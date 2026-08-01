package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// downgradeProjectConfig rewrites a project configuration as the version 1
// document an existing clone still has on disk.
func downgradeProjectConfig(t *testing.T, repository string) {
	t.Helper()
	path := filepath.Join(repository, ".workbook", "config.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"format":"workbook.project","version":1,` +
		string(contents[len(`{"format":"workbook.project","version":2,`):]))
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A repository initialized before the automatic synchronization policy still
// holds a version 1 configuration, and Init never rewrites one. Every ordinary
// command has to keep working against it.
func TestLegacyProjectConfigStillReadsAndWrites(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup = code %d, stderr %q", code, stderr)
	}
	downgradeProjectConfig(t, repository)

	code, stdout, stderr := run(t, repository, "create", "Legacy configuration task", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")

	if code, _, stderr := run(t, repository, "list", "--json"); code != 0 {
		t.Fatalf("list = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "update", task.ID, "--status", "ready", "--json"); code != 0 {
		t.Fatalf("update = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "show", task.ID, "--json"); code != 0 {
		t.Fatalf("show = code %d, stderr %q", code, stderr)
	}
}

func projectConfigVersion(t *testing.T, repository string) int {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repository, ".workbook", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Version  int   `json:"version"`
		AutoSync *bool `json:"autoSync"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	return document.Version
}

func TestSetupUpgradesALegacyProjectConfiguration(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup = code %d, stderr %q", code, stderr)
	}
	downgradeProjectConfig(t, repository)

	code, stdout, stderr := run(t, repository, "setup", "--no-docs", "--json")
	if code != 0 {
		t.Fatalf("setup = code %d, stderr %q", code, stderr)
	}
	var envelope struct {
		Data struct {
			Config struct {
				Version  int  `json:"version"`
				Upgraded bool `json:"upgraded"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode setup result: %v; output = %q", err, stdout)
	}
	if !envelope.Data.Config.Upgraded || envelope.Data.Config.Version != 2 {
		t.Fatalf("setup config report = %+v, want upgraded to version 2", envelope.Data.Config)
	}
	if got := projectConfigVersion(t, repository); got != 2 {
		t.Fatalf("on-disk version = %d, want 2", got)
	}

	code, stdout, stderr = run(t, repository, "setup", "--no-docs", "--json")
	if code != 0 {
		t.Fatalf("second setup = code %d, stderr %q", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Config.Upgraded {
		t.Fatal("second setup reported an upgrade, want idempotent")
	}
}

func TestConfigSetAutoSyncControlsTheProjectPolicy(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup = code %d, stderr %q", code, stderr)
	}
	downgradeProjectConfig(t, repository)

	if code, _, stderr := run(t, repository, "config", "set", "auto-sync", "false", "--json"); code != 0 {
		t.Fatalf("config set = code %d, stderr %q", code, stderr)
	}
	if got := projectConfigVersion(t, repository); got != 2 {
		t.Fatalf("on-disk version = %d, want 2 after writing a policy", got)
	}

	code, stdout, stderr := run(t, repository, "create", "Policy disabled", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	envelope := decodeAutoSync(t, stdout)
	if envelope.Sync == nil || envelope.Sync.Enabled || envelope.Sync.Source != "project" {
		t.Fatalf("sync = %+v, want disabled by project policy", envelope.Sync)
	}

	if code, _, stderr := run(t, repository, "config", "unset", "auto-sync", "--json"); code != 0 {
		t.Fatalf("config unset = code %d, stderr %q", code, stderr)
	}
	code, stdout, stderr = run(t, repository, "create", "Policy cleared", "--json")
	if code != 0 {
		t.Fatalf("create = code %d, stderr %q", code, stderr)
	}
	envelope = decodeAutoSync(t, stdout)
	if envelope.Sync == nil || !envelope.Sync.Enabled || envelope.Sync.Source != "default" {
		t.Fatalf("sync = %+v, want the default policy after unset", envelope.Sync)
	}
}

func TestConfigShowReportsTheResolvedPolicyAndItsSource(t *testing.T) {
	repository := testrepo.New(t)
	if code, _, stderr := run(t, repository, "setup", "--no-docs"); code != 0 {
		t.Fatalf("setup = code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "config", "set", "auto-sync", "false", "--json"); code != 0 {
		t.Fatalf("config set = code %d, stderr %q", code, stderr)
	}

	code, stdout, stderr := run(t, repository, "config", "show", "--json")
	if code != 0 {
		t.Fatalf("config show = code %d, stderr %q", code, stderr)
	}
	var envelope struct {
		Data struct {
			AutoSync struct {
				Enabled bool   `json:"enabled"`
				Source  string `json:"source"`
			} `json:"autoSync"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode config show: %v; output = %q", err, stdout)
	}
	if envelope.Data.AutoSync.Enabled || envelope.Data.AutoSync.Source != "project" {
		t.Fatalf("config show autoSync = %+v, want disabled by project", envelope.Data.AutoSync)
	}
}
