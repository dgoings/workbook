package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// mintingForbidden fails the calling test if Init reaches for a new project
// ID, proving an adopted identity was used instead of a minted one.
func mintingForbidden(t *testing.T) core.IDSource {
	return core.IDSourceFunc(func() (string, error) {
		t.Fatal("Init() minted a new project ID for a project origin already carries")
		return "", nil
	})
}

// adoptOrigin builds the reported bug's shape: origin's default branch gained
// its tracked Workbook configuration after this clone stopped fetching, so the
// working tree and the stale remote-tracking refs both lack it. It returns the
// stale clone and the configuration origin carries.
func adoptOrigin(t *testing.T, key string) (*Repository, core.ProjectConfig) {
	t.Helper()
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "origin.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	syncGit(t, bare, "config", "receive.autogc", "false")
	syncGit(t, bare, "config", "gc.auto", "0")
	syncGit(t, bare, "config", "maintenance.auto", "false")

	seedPath := testrepo.New(t)
	syncGit(t, seedPath, "branch", "-M", "main")
	if err := os.WriteFile(filepath.Join(seedPath, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncGit(t, seedPath, "add", "README.md")
	syncGit(t, seedPath, "commit", "--quiet", "-m", "Before Workbook")
	syncGit(t, seedPath, "remote", "add", "origin", bare)
	syncGit(t, seedPath, "push", "--quiet", "-u", "origin", "main")
	syncGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	stale := openSyncClone(t, bare)

	seed, err := Open(ctx, seedPath)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := seed.Init(ctx, key, core.CryptoULIDSource{})
	if err != nil {
		t.Fatal(err)
	}
	syncGit(t, seedPath, "add", ".workbook/config.json")
	syncGit(t, seedPath, "commit", "--quiet", "-m", "Initialize Workbook")
	syncGit(t, seedPath, "push", "--quiet", "origin", "main")
	return stale, config
}

func TestAdoptOriginProjectAdoptsCommittedConfigFromStaleCheckout(t *testing.T) {
	ctx := context.Background()
	stale, config := adoptOrigin(t, "WB")

	adopted, found, err := stale.AdoptOriginProject(ctx, "WB")
	if err != nil {
		t.Fatalf("AdoptOriginProject() error = %v", err)
	}
	if !found {
		t.Fatal("AdoptOriginProject() found = false, want true")
	}
	if adopted != config {
		t.Fatalf("AdoptOriginProject() = %#v, want origin's %#v", adopted, config)
	}

	tracked, exists, err := stale.readConfig()
	if err != nil {
		t.Fatalf("readConfig() after adoption error = %v", err)
	}
	if !exists {
		t.Fatal("adoption did not materialize the tracked configuration")
	}
	if tracked != config {
		t.Fatalf("tracked configuration = %#v, want origin's %#v", tracked, config)
	}

	initialized, created, err := stale.Init(ctx, "WB", mintingForbidden(t))
	if err != nil {
		t.Fatalf("Init() after adoption error = %v", err)
	}
	if created {
		t.Fatal("Init() created = true, want adoption of the existing project")
	}
	if initialized != config {
		t.Fatalf("Init() after adoption = %#v, want %#v", initialized, config)
	}
}

func TestAdoptOriginProjectWithoutOriginIsNoOp(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	_, found, err := repo.AdoptOriginProject(context.Background(), "WB")
	if err != nil {
		t.Fatalf("AdoptOriginProject() error = %v", err)
	}
	if found {
		t.Fatal("AdoptOriginProject() found = true, want false without an origin")
	}
	if _, exists, _ := repo.readConfig(); exists {
		t.Fatal("AdoptOriginProject() wrote a tracked configuration without an origin")
	}
}

func TestAdoptOriginProjectEmptyOriginIsNoOp(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "origin.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	repoDir := testrepo.New(t)
	syncGit(t, repoDir, "remote", "add", "origin", bare)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	_, found, err := repo.AdoptOriginProject(context.Background(), "WB")
	if err != nil {
		t.Fatalf("AdoptOriginProject() error = %v", err)
	}
	if found {
		t.Fatal("AdoptOriginProject() found = true, want false for an empty origin")
	}
}

func TestAdoptOriginProjectUnreachableOriginFails(t *testing.T) {
	repoDir := testrepo.New(t)
	syncGit(t, repoDir, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = repo.AdoptOriginProject(context.Background(), "WB")
	if err == nil {
		t.Fatal("AdoptOriginProject() error = nil, want an operational failure")
	}
	if core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("AdoptOriginProject() category = %v, want operational", core.CategoryOf(err))
	}
}

func TestAdoptOriginProjectExistingTrackedConfigIsNoOp(t *testing.T) {
	ctx := context.Background()
	repoDir := testrepo.New(t)
	syncGit(t, repoDir, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing.git"))
	repo, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Init(ctx, "WB", fixedIDs()); err != nil {
		t.Fatal(err)
	}

	// The unreachable origin proves an initialized repository never probes.
	_, found, err := repo.AdoptOriginProject(ctx, "WB")
	if err != nil {
		t.Fatalf("AdoptOriginProject() error = %v", err)
	}
	if found {
		t.Fatal("AdoptOriginProject() found = true, want false when already initialized")
	}
}

func TestAdoptOriginProjectKeyMismatchFails(t *testing.T) {
	ctx := context.Background()
	stale, config := adoptOrigin(t, "AB")

	_, _, err := stale.AdoptOriginProject(ctx, "WB")
	if err == nil {
		t.Fatal("AdoptOriginProject() error = nil, want a validation failure")
	}
	if core.CategoryOf(err) != core.CategoryValidation {
		t.Fatalf("AdoptOriginProject() category = %v, want validation", core.CategoryOf(err))
	}
	if !strings.Contains(err.Error(), config.Key) {
		t.Fatalf("AdoptOriginProject() error %q does not name origin's key %q", err, config.Key)
	}
	if _, exists, _ := stale.readConfig(); exists {
		t.Fatal("a rejected adoption must not write the tracked configuration")
	}
}

func TestAdoptOriginProjectTaskRefsWithoutCommittedConfigFails(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "origin.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	syncGit(t, bare, "config", "receive.autogc", "false")
	syncGit(t, bare, "config", "gc.auto", "0")
	syncGit(t, bare, "config", "maintenance.auto", "false")

	seedPath := testrepo.New(t)
	syncGit(t, seedPath, "branch", "-M", "main")
	if err := os.WriteFile(filepath.Join(seedPath, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncGit(t, seedPath, "add", "README.md")
	syncGit(t, seedPath, "commit", "--quiet", "-m", "Before Workbook")
	syncGit(t, seedPath, "remote", "add", "origin", bare)
	syncGit(t, seedPath, "push", "--quiet", "-u", "origin", "main")
	syncGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")

	// The seed publishes task refs but never commits its configuration, so
	// origin's default branch cannot say which project the tasks belong to.
	seed, err := Open(ctx, seedPath)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := seed.Init(ctx, "WB", core.CryptoULIDSource{})
	if err != nil {
		t.Fatal(err)
	}
	createSyncTask(t, seed, config, "Published without a committed configuration")
	publishTaskRefs(t, seed)

	stale := openSyncClone(t, bare)
	_, _, err = stale.AdoptOriginProject(ctx, "WB")
	if err == nil {
		t.Fatal("AdoptOriginProject() error = nil, want a refusal to mint a second project")
	}
	if core.CategoryOf(err) != core.CategoryOperational {
		t.Fatalf("AdoptOriginProject() category = %v, want operational", core.CategoryOf(err))
	}
	if !strings.Contains(err.Error(), ".workbook/config.json") {
		t.Fatalf("AdoptOriginProject() error %q does not tell the user what origin is missing", err)
	}
	if _, exists, _ := stale.readConfig(); exists {
		t.Fatal("a refused adoption must not write the tracked configuration")
	}
	if _, exists, _ := stale.readProjectGuard(); exists {
		t.Fatal("a refused adoption must not publish the common project guard")
	}
}
