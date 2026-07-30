// Package userconfig reads and writes Workbook's user-global configuration.
//
// Unlike the project configuration in .workbook/config.json, this file is
// hand-edited, never synchronized, and never travels with a clone. It records
// preferences that apply to every project a developer works in.
package userconfig

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dgoings/workbook/internal/core"
)

const (
	// Format identifies the user configuration document.
	Format = "workbook.user"
	// Version is the supported user configuration version.
	Version = 1

	directoryName = "workbook"
	fileName      = "config.json"
)

// Config is Workbook's user-global configuration.
type Config struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// DocTargets names the agent documentation files Workbook manages. A
	// target is refreshed only when the project already contains it;
	// Workbook never creates one on its own.
	DocTargets []string `json:"docTargets"`
	// SkillDir is where the Workbook skill is installed. A relative value
	// resolves against the project root, an absolute value against the
	// filesystem, which allows a single personal copy across projects.
	SkillDir string `json:"skillDir"`
	// Preferences carries forward-compatible settings. It is deliberately
	// untyped so that adding a preference never requires a version bump.
	Preferences map[string]any `json:"preferences"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{
		Format:      Format,
		Version:     Version,
		DocTargets:  []string{"AGENTS.md", "CLAUDE.md"},
		SkillDir:    filepath.Join(".claude", "skills"),
		Preferences: map[string]any{},
	}
}

// Path reports the user configuration location, honouring XDG_CONFIG_HOME.
func Path() (string, error) {
	if directory := os.Getenv("XDG_CONFIG_HOME"); directory != "" {
		return filepath.Join(directory, directoryName, fileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", core.Wrap(core.CategoryOperational, "determine home directory", err)
	}
	return filepath.Join(home, ".config", directoryName, fileName), nil
}

// Load reads the user configuration, returning defaults when it is absent.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, core.Wrap(core.CategoryOperational, "read user configuration", err)
	}
	return decode(path, contents)
}

// Save writes the user configuration, creating its directory when needed.
func Save(config Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	contents, err := encode(config)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "create user configuration directory", err)
	}
	temporary, err := os.CreateTemp(directory, ".workbook-config.*")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "create user configuration", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "write user configuration", err)
	}
	if err := temporary.Close(); err != nil {
		return core.Wrap(core.CategoryOperational, "write user configuration", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return core.Wrap(core.CategoryOperational, "set user configuration mode", err)
	}
	if err := os.Rename(name, path); err != nil {
		return core.Wrap(core.CategoryOperational, "publish user configuration", err)
	}
	return nil
}

// Ensure loads the user configuration, writing defaults when it is absent. It
// reports whether the file was created so callers can tell the user where to
// find it. An existing file is never rewritten.
func Ensure() (Config, bool, error) {
	path, err := Path()
	if err != nil {
		return Config{}, false, err
	}
	if _, err := os.Stat(path); err == nil {
		config, err := Load()
		return config, false, err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Config{}, false, core.Wrap(core.CategoryOperational, "inspect user configuration", err)
	}

	config := Default()
	if err := Save(config); err != nil {
		return Config{}, false, err
	}
	return config, true, nil
}

func decode(path string, contents []byte) (Config, error) {
	var config Config
	if err := json.Unmarshal(contents, &config); err != nil {
		return Config{}, core.Wrap(core.CategoryCorruptData, "parse user configuration "+path, err)
	}
	if config.Format != Format {
		return Config{}, core.Errorf(core.CategoryCorruptData, "user configuration %s has format %q, want %q", path, config.Format, Format)
	}
	if config.Version != Version {
		return Config{}, core.Errorf(core.CategoryCorruptData, "user configuration %s has version %d, want %d", path, config.Version, Version)
	}
	if config.Preferences == nil {
		config.Preferences = map[string]any{}
	}
	return config, nil
}

func encode(config Config) ([]byte, error) {
	if config.Preferences == nil {
		config.Preferences = map[string]any{}
	}
	if config.DocTargets == nil {
		config.DocTargets = []string{}
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "encode user configuration", err)
	}
	return append(contents, '\n'), nil
}
