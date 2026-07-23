package gitstore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

const fixedProjectID = "01K0M65GBZ8F5ZQX0VC1J8H3TP"

func fixedIDs() core.IDSource {
	return core.IDSourceFunc(func() (string, error) { return fixedProjectID, nil })
}

func TestInitCreatesTrackedConfigAndPrivateCache(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	config, created, err := repo.Init(context.Background(), "WB", fixedIDs())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if !created {
		t.Fatal("Init() created = false, want true")
	}
	wantConfig := core.ProjectConfig{
		Format: "workbook.project", Version: 1, ProjectID: fixedProjectID, Key: "WB",
	}
	if config != wantConfig {
		t.Fatalf("Init() config = %#v, want %#v", config, wantConfig)
	}

	configFile := filepath.Join(repoDir, configPath)
	contents, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	wantContents := []byte(`{"format":"workbook.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB"}` + "\n")
	if !bytes.Equal(contents, wantContents) {
		t.Fatalf("config contents = %q, want %q", contents, wantContents)
	}
	if got := bytes.Count(contents, []byte{'\n'}); got != 1 || contents[len(contents)-1] != '\n' {
		t.Fatalf("config must contain one trailing LF, got %q", contents)
	}
	info, err := os.Stat(configFile)
	if err != nil {
		t.Fatalf("Stat(config) error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Fatalf("config mode = %o, want %o", got, want)
	}
	cacheInfo, err := os.Stat(filepath.Join(repo.CommonGitDir, "workbook"))
	if err != nil {
		t.Fatalf("Stat(private cache) error = %v", err)
	}
	if !cacheInfo.IsDir() {
		t.Fatalf("private cache is not a directory")
	}
	if got, want := cacheInfo.Mode().Perm(), os.FileMode(0o755); got != want {
		t.Fatalf("private cache mode = %o, want %o", got, want)
	}
	if output, err := repo.Git(context.Background(), nil, "for-each-ref", "refs/workbook/tasks"); err != nil {
		t.Fatalf("Git(for-each-ref) error = %v", err)
	} else if len(output) != 0 {
		t.Fatalf("Init() created task refs: %q", output)
	}
}

func TestInitIsIdempotentForTheSameKey(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, created, err := repo.Init(context.Background(), "WB", fixedIDs()); err != nil || !created {
		t.Fatalf("first Init() = (_, %t, %v), want (_, true, nil)", created, err)
	}
	before, err := os.ReadFile(filepath.Join(repoDir, configPath))
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}

	config, created, err := repo.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		t.Fatal("Init() asked for a new ID despite existing config")
		return "", nil
	}))
	if err != nil {
		t.Fatalf("second Init() error = %v", err)
	}
	if created {
		t.Fatal("second Init() created = true, want false")
	}
	if got, want := config.ProjectID, fixedProjectID; got != want {
		t.Fatalf("second Init() project ID = %q, want %q", got, want)
	}
	after, err := os.ReadFile(filepath.Join(repoDir, configPath))
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("second Init() rewrote config: before %q, after %q", before, after)
	}
}

func TestInitRejectsConflictingKeyWithoutRewriting(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, _, err := repo.Init(context.Background(), "WB", fixedIDs()); err != nil {
		t.Fatalf("first Init() error = %v", err)
	}
	configFile := filepath.Join(repoDir, configPath)
	before, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}

	_, _, err = repo.Init(context.Background(), "OTHER", fixedIDs())
	if got, want := core.CategoryOf(err), core.CategoryValidation; got != want {
		t.Fatalf("Init() category = %q, want %q; error = %v", got, want, err)
	}
	after, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("Init() rewrote conflicting config: before %q, after %q", before, after)
	}
}

func TestInitRejectsCorruptExistingConfig(t *testing.T) {
	tests := map[string][]byte{
		"malformed":          []byte(`{"format":`),
		"foreign format":     []byte(`{"format":"other.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB"}`),
		"foreign version":    []byte(`{"format":"workbook.project","version":2,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB"}`),
		"invalid project ID": []byte(`{"format":"workbook.project","version":1,"projectId":"not-a-ulid","key":"WB"}`),
		"invalid key":        []byte(`{"format":"workbook.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"wb"}`),
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			repoDir := testrepo.New(t)
			repo, err := Open(context.Background(), repoDir)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			configFile := filepath.Join(repoDir, configPath)
			if err := os.MkdirAll(filepath.Dir(configFile), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err := os.WriteFile(configFile, contents, 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			_, _, err = repo.Init(context.Background(), "WB", fixedIDs())
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("Init() category = %q, want %q; error = %v", got, want, err)
			}
			after, err := os.ReadFile(configFile)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if !bytes.Equal(after, contents) {
				t.Fatalf("Init() rewrote corrupt config: before %q, after %q", contents, after)
			}
		})
	}
}

func TestLoadConfigReturnsExistingConfig(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want, _, err := repo.Init(context.Background(), "WB", fixedIDs())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	got, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got != want {
		t.Fatalf("LoadConfig() = %#v, want %#v", got, want)
	}
}
