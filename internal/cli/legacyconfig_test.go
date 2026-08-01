package cli

import (
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
