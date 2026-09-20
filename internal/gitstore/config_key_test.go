package gitstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestInitWithNoRequestedKeyAdoptsTheExistingOne(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, _, err := repo.Init(context.Background(), "PROJ", fixedIDs()); err != nil {
		t.Fatalf("first Init() error = %v", err)
	}

	config, created, err := repo.Init(context.Background(), "", core.IDSourceFunc(func() (string, error) {
		t.Fatal("Init() asked for a new ID despite existing config")
		return "", nil
	}))
	if err != nil {
		t.Fatalf("Init() with no key error = %v", err)
	}
	if created {
		t.Fatal("Init() with no key created = true, want false")
	}
	if config.Key != "PROJ" {
		t.Fatalf("Init() with no key adopted key %q, want %q", config.Key, "PROJ")
	}
}

func TestInitWithNoRequestedKeyRefusesToMint(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	_, _, err = repo.Init(context.Background(), "", core.IDSourceFunc(func() (string, error) {
		t.Fatal("Init() minted without a key to mint under")
		return "", nil
	}))
	if core.CategoryOf(err) != core.CategoryInvocation {
		t.Fatalf("Init() with no key on an empty repository error = %v, want an invocation failure", err)
	}
	if _, exists, _ := repo.readConfig(); exists {
		t.Fatal("a refused mint must not write the tracked configuration")
	}
}

func TestHasProjectIdentity(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	has, err := repo.HasProjectIdentity(context.Background())
	if err != nil {
		t.Fatalf("HasProjectIdentity() error = %v", err)
	}
	if has {
		t.Fatal("HasProjectIdentity() = true on an empty repository")
	}
	if _, _, err := repo.Init(context.Background(), "WB", fixedIDs()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	has, err = repo.HasProjectIdentity(context.Background())
	if err != nil {
		t.Fatalf("HasProjectIdentity() after Init error = %v", err)
	}
	if !has {
		t.Fatal("HasProjectIdentity() = false after Init")
	}
}

func TestHasProjectIdentityIsTrueForTrackedConfigAlone(t *testing.T) {
	repoDir := testrepo.New(t)
	repo, err := Open(context.Background(), repoDir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repo.Root, configPath), want)

	has, err := repo.HasProjectIdentity(context.Background())
	if err != nil {
		t.Fatalf("HasProjectIdentity() error = %v", err)
	}
	if !has {
		t.Fatal("HasProjectIdentity() = false with only a tracked configuration on disk")
	}
}

func TestHasProjectIdentityIsTrueForTheCommonProjectGuardAlone(t *testing.T) {
	repo, err := Open(context.Background(), testrepo.New(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	want := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", "project.json"), want)

	has, err := repo.HasProjectIdentity(context.Background())
	if err != nil {
		t.Fatalf("HasProjectIdentity() error = %v", err)
	}
	if !has {
		t.Fatal("HasProjectIdentity() = false with only the common project guard on disk")
	}
}

func TestAdoptOriginProjectWithNoRequestedKeyJoinsWhateverOriginHas(t *testing.T) {
	ctx := context.Background()
	stale, config := adoptOrigin(t, "AB")

	adopted, found, err := stale.AdoptOriginProject(ctx, "")
	if err != nil {
		t.Fatalf("AdoptOriginProject() error = %v", err)
	}
	if !found {
		t.Fatal("AdoptOriginProject() found = false, want true")
	}
	if adopted.Key != config.Key || adopted.ProjectID != config.ProjectID {
		t.Fatalf("AdoptOriginProject() = %+v, want origin's %+v", adopted, config)
	}
	if _, _, err := stale.Init(ctx, "", mintingForbidden(t)); err != nil {
		t.Fatalf("Init() after adoption error = %v", err)
	}
}
