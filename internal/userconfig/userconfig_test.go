package userconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func TestPathPrefersXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if want := filepath.Join(xdg, "workbook", "config.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestPathFallsBackToHomeConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := Path()
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if want := filepath.Join(home, ".config", "workbook", "config.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoadReturnsDefaultsWhenFileIsAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := Default(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestDefaultRefreshesBothAgentDocumentationTargets(t *testing.T) {
	config := Default()
	if want := []string{"AGENTS.md", "CLAUDE.md"}; !reflect.DeepEqual(config.DocTargets, want) {
		t.Fatalf("Default().DocTargets = %#v, want %#v", config.DocTargets, want)
	}
	if want := filepath.Join(".claude", "skills"); config.SkillDir != want {
		t.Fatalf("Default().SkillDir = %q, want %q", config.SkillDir, want)
	}
	if config.Preferences == nil {
		t.Fatal("Default().Preferences = nil, want an empty map")
	}
	if len(config.Preferences) != 0 {
		t.Fatalf("Default().Preferences = %#v, want empty", config.Preferences)
	}
}

func TestSaveThenLoadPreservesUnknownPreferenceKeys(t *testing.T) {
	// Production mutation: rejecting unknown preference keys the way project
	// config does would make every future preference a breaking format change.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	config := Default()
	config.Preferences = map[string]any{"futureOption": "enabled"}
	if err := Save(config); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := map[string]any{"futureOption": "enabled"}; !reflect.DeepEqual(got.Preferences, want) {
		t.Fatalf("Load().Preferences = %#v, want %#v", got.Preferences, want)
	}
}

func TestSaveCreatesTheConfigurationDirectory(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", xdg)

	if err := Save(Default()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	path := filepath.Join(xdg, "workbook", "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("config mode = %o, want 644", got)
	}
}

func TestLoadRejectsUnsupportedFormatAndVersion(t *testing.T) {
	for name, contents := range map[string]string{
		"format":  `{"format":"workbook.project","version":1,"docTargets":[],"skillDir":"s","preferences":{}}`,
		"version": `{"format":"workbook.user","version":2,"docTargets":[],"skillDir":"s","preferences":{}}`,
		"invalid": `{"format":`,
	} {
		t.Run(name, func(t *testing.T) {
			xdg := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", xdg)
			directory := filepath.Join(xdg, "workbook")
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatalf("create directory: %v", err)
			}
			if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(contents), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := Load()
			if err == nil {
				t.Fatal("Load() succeeded, want a corrupt-data error")
			}
			if got := core.CategoryOf(err); got != core.CategoryCorruptData {
				t.Fatalf("Load() category = %q, want %q", got, core.CategoryCorruptData)
			}
		})
	}
}

func TestEnsureWritesDefaultsOnlyWhenAbsent(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", xdg)

	config, created, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !created {
		t.Fatal("Ensure() created = false on first call, want true")
	}
	if want := Default(); !reflect.DeepEqual(config, want) {
		t.Fatalf("Ensure() = %#v, want %#v", config, want)
	}

	// Production mutation: rewriting defaults on every call would silently
	// discard a user's edited preferences.
	edited := Default()
	edited.DocTargets = []string{"AGENTS.md"}
	if err := Save(edited); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	config, created, err = Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if created {
		t.Fatal("Ensure() created = true on second call, want false")
	}
	if want := []string{"AGENTS.md"}; !reflect.DeepEqual(config.DocTargets, want) {
		t.Fatalf("Ensure().DocTargets = %#v, want %#v", config.DocTargets, want)
	}
}
