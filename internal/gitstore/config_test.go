package gitstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

const fixedProjectID = "01K0M65GBZ8F5ZQX0VC1J8H3TP"

func fixedIDs() core.IDSource {
	return core.IDSourceFunc(func() (string, error) { return fixedProjectID, nil })
}

func idsFor(projectID string) core.IDSource {
	return core.IDSourceFunc(func() (string, error) { return projectID, nil })
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

func TestInitClassifiesProjectIDGenerationFailureAsOperational(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	cause := errors.New("entropy failed")

	_, _, err = repo.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		return "", cause
	}))
	if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
		t.Fatalf("Init() category = %q, want %q; error = %v", got, want, err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Init() error = %v, want cause %v", err, cause)
	}
	assertPathMissing(t, filepath.Join(repo.Root, configPath))
	assertPathMissing(t, filepath.Join(repo.CommonGitDir, "workbook", projectGuard))
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

func TestInitPublishesTrackedConfigAsCommonProjectGuard(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repo.Root, configPath), want)

	got, created, err := repo.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		t.Fatal("Init() generated an ID despite tracked configuration")
		return "", nil
	}))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if created {
		t.Fatal("Init() created = true, want false for existing tracked identity")
	}
	if got != want {
		t.Fatalf("Init() config = %#v, want %#v", got, want)
	}
	assertProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", "project.json"), want)
}

func TestInitRestoresTrackedConfigFromCommonProjectGuard(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", "project.json"), want)

	got, created, err := repo.Init(context.Background(), "WB", core.IDSourceFunc(func() (string, error) {
		t.Fatal("Init() generated an ID despite common project guard")
		return "", nil
	}))
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if created {
		t.Fatal("Init() created = true, want false for recovered identity")
	}
	if got != want {
		t.Fatalf("Init() config = %#v, want %#v", got, want)
	}
	assertProjectConfigFile(t, filepath.Join(repo.Root, configPath), want)
}

func TestInitRejectsMismatchedTrackedConfigAndCommonProjectGuard(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	tracked := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	guard := tracked
	guard.ProjectID = "01K0M65GBZ8F5ZQX0VC1J8H3TQ"
	writeProjectConfigFile(t, filepath.Join(repo.Root, configPath), tracked)
	writeProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", "project.json"), guard)

	_, _, err = repo.Init(context.Background(), "WB", fixedIDs())
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Init() category = %q, want %q; error = %v", got, want, err)
	}
	assertProjectConfigFile(t, filepath.Join(repo.Root, configPath), tracked)
	assertProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", "project.json"), guard)
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
		"malformed":             []byte(`{"format":`),
		"foreign format":        []byte(`{"format":"other.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB"}`),
		"foreign version":       []byte(`{"format":"workbook.project","version":2,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB"}`),
		"invalid project ID":    []byte(`{"format":"workbook.project","version":1,"projectId":"not-a-ulid","key":"WB"}`),
		"invalid key":           []byte(`{"format":"workbook.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"wb"}`),
		"valid JSON without LF": []byte(`{"format":"workbook.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB"}`),
		"pretty JSON":           []byte("{\n  \"format\": \"workbook.project\",\n  \"version\": 1,\n  \"projectId\": \"01K0M65GBZ8F5ZQX0VC1J8H3TP\",\n  \"key\": \"WB\"\n}\n"),
		"duplicate member":      []byte(`{"format":"workbook.project","version":1,"projectId":"01K0M65GBZ8F5ZQX0VC1J8H3TP","key":"WB","key":"WB"}` + "\n"),
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

func TestLoadConfigPublishesTrackedIdentityAsMissingCommonGuard(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repo.Root, configPath), want)

	got, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got != want {
		t.Fatalf("LoadConfig() = %#v, want %#v", got, want)
	}
	assertProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", projectGuard), want)
}

func TestLoadConfigRejectsMismatchedCommonProjectGuard(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	tracked := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	guard := tracked
	guard.ProjectID = "01K0M65GBZ8F5ZQX0VC1J8H3TQ"
	writeProjectConfigFile(t, filepath.Join(repo.Root, configPath), tracked)
	writeProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", projectGuard), guard)

	_, err = repo.LoadConfig()
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("LoadConfig() category = %q, want %q; error = %v", got, want, err)
	}
}

func TestLoadConfigRequiresInitWhenOnlyCommonProjectGuardExists(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	guard := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", projectGuard), guard)

	_, err = repo.LoadConfig()
	if got, want := core.CategoryOf(err), core.CategoryNotInitialized; got != want {
		t.Fatalf("LoadConfig() category = %q, want %q; error = %v", got, want, err)
	}
	assertPathMissing(t, filepath.Join(repo.Root, configPath))
}

func TestConfigurationFilesystemFailuresAreOperational(t *testing.T) {
	config := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}

	t.Run("read tracked config", func(t *testing.T) {
		repo, err := Open(context.Background(), testrepo.New(t))
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		if err := os.MkdirAll(filepath.Join(repo.Root, configPath), 0o755); err != nil {
			t.Fatalf("MkdirAll(config path) error = %v", err)
		}

		_, err = repo.LoadConfig()
		if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
			t.Fatalf("LoadConfig() category = %q, want %q; error = %v", got, want, err)
		}
	})

	t.Run("write tracked config", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".workbook"), []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("WriteFile(.workbook) error = %v", err)
		}
		repo := &Repository{Root: root}

		err := repo.writeConfig(config)
		if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
			t.Fatalf("writeConfig() category = %q, want %q; error = %v", got, want, err)
		}
	})

	t.Run("create private cache", func(t *testing.T) {
		commonGitDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(commonGitDir, "workbook"), []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("WriteFile(workbook) error = %v", err)
		}
		repo := &Repository{CommonGitDir: commonGitDir}

		err := repo.ensurePrivateCache()
		if got, want := core.CategoryOf(err), core.CategoryOperational; got != want {
			t.Fatalf("ensurePrivateCache() category = %q, want %q; error = %v", got, want, err)
		}
	})
}

func TestInitRejectsInvalidConstructedRepository(t *testing.T) {
	baseDir := t.TempDir()
	repository := &Repository{
		Root:         filepath.Join(baseDir, "not-a-repository"),
		CommonGitDir: filepath.Join(baseDir, "not-a-git-directory"),
	}

	_, _, err := repository.Init(context.Background(), "WB", fixedIDs())
	if got, want := core.CategoryOf(err), core.CategoryNotInitialized; got != want {
		t.Fatalf("Init() category = %q, want %q; error = %v", got, want, err)
	}
	assertPathMissing(t, filepath.Join(repository.Root, ".workbook"))
	assertPathMissing(t, filepath.Join(repository.CommonGitDir, "workbook"))
}

func TestInitConcurrentSameKeyReturnsPersistedConfig(t *testing.T) {
	repoDir := testrepo.New(t)
	results := concurrentInit(t, repoDir,
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TP")},
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TQ")},
	)

	created := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Init() error = %v", result.err)
		}
		if result.created {
			created++
		}
	}
	if got, want := created, 1; got != want {
		t.Fatalf("concurrent Init() created count = %d, want %d", got, want)
	}
	if results[0].config != results[1].config {
		t.Fatalf("concurrent Init() configs differ: %#v and %#v", results[0].config, results[1].config)
	}
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	persisted, err := repo.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if results[0].config != persisted {
		t.Fatalf("concurrent Init() config = %#v, persisted config = %#v", results[0].config, persisted)
	}
}

func TestInitConcurrentSameRepositoryReturnsPersistedConfig(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	repo.gitPath = ""
	results := concurrentInitOnRepository(t, repo,
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TP")},
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TQ")},
	)

	created := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Init() error = %v", result.err)
		}
		if result.created {
			created++
		}
	}
	if got, want := created, 1; got != want {
		t.Fatalf("concurrent Init() created count = %d, want %d", got, want)
	}
	if results[0].config != results[1].config {
		t.Fatalf("concurrent Init() configs differ: %#v and %#v", results[0].config, results[1].config)
	}
}

func TestInitConcurrentLinkedWorktreesSharesOneStableIdentity(t *testing.T) {
	repositories := linkedWorktreeRepositories(t)
	results := concurrentInitOnRepositories(t, repositories,
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TP")},
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TQ")},
	)

	created := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Init() error = %v", result.err)
		}
		if result.created {
			created++
		}
	}
	if got, want := created, 1; got != want {
		t.Fatalf("concurrent Init() created count = %d, want %d", got, want)
	}
	if results[0].config != results[1].config {
		t.Fatalf("linked-worktree configs differ: %#v and %#v", results[0].config, results[1].config)
	}
	for _, repo := range repositories {
		assertProjectConfigFile(t, filepath.Join(repo.Root, configPath), results[0].config)
	}
	assertProjectConfigFile(t, filepath.Join(repositories[0].CommonGitDir, "workbook", "project.json"), results[0].config)
}

func TestInitConcurrentDifferentKeysReturnsValidationError(t *testing.T) {
	repoDir := testrepo.New(t)
	results := concurrentInit(t, repoDir,
		initRequest{key: "WB", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TP")},
		initRequest{key: "OTHER", ids: idsFor("01K0M65GBZ8F5ZQX0VC1J8H3TQ")},
	)

	successes := 0
	validationErrors := 0
	for _, result := range results {
		switch core.CategoryOf(result.err) {
		case "":
			successes++
		case core.CategoryValidation:
			validationErrors++
		default:
			t.Fatalf("concurrent Init() error category = %q, error = %v", core.CategoryOf(result.err), result.err)
		}
	}
	if successes != 1 || validationErrors != 1 {
		t.Fatalf("concurrent Init() successes/errors = %d/%d, want 1/1", successes, validationErrors)
	}
}

func TestInitIgnoresStaleWorktreeLocalInitializationLock(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	lockDir := filepath.Join(repoDir, ".workbook", ".init.lock")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(lock) error = %v", err)
	}

	config, created, err := repo.Init(context.Background(), "WB", fixedIDs())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if !created {
		t.Fatal("Init() created = false, want true")
	}
	assertProjectConfigFile(t, filepath.Join(repoDir, configPath), config)
	assertProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", "project.json"), config)
}

type initRequest struct {
	key string
	ids core.IDSource
}

type initResult struct {
	config  core.ProjectConfig
	created bool
	err     error
}

func concurrentInit(t *testing.T, repoDir string, requests ...initRequest) []initResult {
	t.Helper()
	results := make([]initResult, len(requests))
	start := make(chan struct{})
	ready := make(chan struct{}, len(requests))
	var wait sync.WaitGroup

	for index, request := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			repo, err := Open(context.Background(), repoDir)
			if err != nil {
				results[index].err = err
				ready <- struct{}{}
				return
			}
			ready <- struct{}{}
			<-start
			results[index].config, results[index].created, results[index].err = repo.Init(context.Background(), request.key, request.ids)
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	wait.Wait()
	return results
}

func concurrentInitOnRepository(t *testing.T, repo *Repository, requests ...initRequest) []initResult {
	t.Helper()
	results := make([]initResult, len(requests))
	start := make(chan struct{})
	ready := make(chan struct{}, len(requests))
	var wait sync.WaitGroup

	for index, request := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ready <- struct{}{}
			<-start
			results[index].config, results[index].created, results[index].err = repo.Init(context.Background(), request.key, request.ids)
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	wait.Wait()
	return results
}

func concurrentInitOnRepositories(t *testing.T, repositories []*Repository, requests ...initRequest) []initResult {
	t.Helper()
	if len(repositories) != len(requests) {
		t.Fatalf("repository count = %d, request count = %d", len(repositories), len(requests))
	}
	results := make([]initResult, len(requests))
	start := make(chan struct{})
	ready := make(chan struct{}, len(requests))
	var wait sync.WaitGroup

	for index, request := range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ready <- struct{}{}
			<-start
			results[index].config, results[index].created, results[index].err =
				repositories[index].Init(context.Background(), request.key, request.ids)
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	wait.Wait()
	return results
}

func linkedWorktreeRepositories(t *testing.T) []*Repository {
	t.Helper()
	repoDir := testrepo.New(t)
	gitRun(t, repoDir, "commit", "--allow-empty", "--quiet", "-m", "initial")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	gitRun(t, repoDir, "worktree", "add", "--detach", "--quiet", linkedDir, "HEAD")
	t.Cleanup(func() { gitRun(t, repoDir, "worktree", "remove", "--force", linkedDir) })

	repositories := make([]*Repository, 0, 2)
	for _, directory := range []string{repoDir, linkedDir} {
		repo, err := Open(context.Background(), directory)
		if err != nil {
			t.Fatalf("Open(%q) error = %v", directory, err)
		}
		repositories = append(repositories, repo)
	}
	if repositories[0].CommonGitDir != repositories[1].CommonGitDir {
		t.Fatalf("linked worktrees have different common Git directories: %q and %q",
			repositories[0].CommonGitDir,
			repositories[1].CommonGitDir,
		)
	}
	return repositories
}

func writeProjectConfigFile(t *testing.T, path string, config core.ProjectConfig) {
	t.Helper()
	contents, err := encodeConfig(config)
	if err != nil {
		t.Fatalf("encodeConfig() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func assertProjectConfigFile(t *testing.T, path string, want core.ProjectConfig) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	got, err := decodeConfig(contents)
	if err != nil {
		t.Fatalf("decodeConfig(%q) error = %v", path, err)
	}
	if got != want {
		t.Fatalf("config at %q = %#v, want %#v", path, got, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", path, err)
	}
}
