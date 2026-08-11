package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

const foreignProjectID = "01K0M65GBZ8F5ZQX0VC1J8H3TQ"

// identityDocumentBytes is the exact canonical byte sequence Workbook stores in
// the identity ref. Tests build it by hand so the stored format is pinned
// independently of the encoder under test.
func identityDocumentBytes(projectID, key string) []byte {
	return []byte(fmt.Sprintf(
		`{"format":"workbook.project-identity","version":1,"projectId":%q,"key":%q}`+"\n",
		projectID, key,
	))
}

// writeIdentityCommit builds the identity object graph with Git plumbing and
// returns the root commit. It deliberately uses no Workbook code so a test can
// state what the ref must look like rather than what this build happens to
// write.
func writeIdentityCommit(t *testing.T, directory, projectID, key string) string {
	t.Helper()
	blob := syncGitInput(t, directory, identityDocumentBytes(projectID, key), "hash-object", "-w", "--stdin")
	tree := syncGitInput(t, directory, []byte(fmt.Sprintf("100644 blob %s\tproject.json\n", blob)), "mktree")
	return syncGit(t, directory, "commit-tree", tree, "-m", "workbook: project identity")
}

func publishIdentityRefByHand(t *testing.T, directory, projectID, key string) string {
	t.Helper()
	commit := writeIdentityCommit(t, directory, projectID, key)
	syncGit(t, directory, "update-ref", identityRef, commit)
	return commit
}

// TestInitPublishesOneRootCommitCarryingOneDocument pins the ref's shape. The
// structure is the invariant readers depend on: no parents means identity has
// no history to disagree about, and one tree entry means the ref has one
// authority.
func TestInitPublishesOneRootCommitCarryingOneDocument(t *testing.T) {
	ctx := context.Background()
	repoDir := testrepo.New(t)
	repo, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.Init(ctx, "WB", fixedIDs()); err != nil || !created {
		t.Fatalf("Init() = (_, %t, %v), want a minted project", created, err)
	}

	head := refValue(t, repo, identityRef)
	if got := parentCount(t, repo, head); got != 0 {
		t.Fatalf("identity commit parents = %d, want 0", got)
	}
	if got := syncGit(t, repoDir, "ls-tree", "--name-only", head); got != identityDocumentPath {
		t.Fatalf("identity tree entries = %q, want exactly %q", got, identityDocumentPath)
	}
	contents := syncGit(t, repoDir, "cat-file", "blob", head+":"+identityDocumentPath)
	if got, want := contents+"\n", string(identityDocumentBytes(fixedProjectID, "WB")); got != want {
		t.Fatalf("identity document = %q, want %q", got, want)
	}
	// A fixed author, committer and date are what make the commit a pure
	// function of the document.
	authorship := syncGit(t, repoDir, "show", "--no-patch", "--format=%an|%ae|%at|%ai|%cn|%ce|%ct|%ci|%s", head)
	want := identityCommitName + "|" + identityCommitEmail + "|0|1970-01-01 00:00:00 +0000|" +
		identityCommitName + "|" + identityCommitEmail + "|0|1970-01-01 00:00:00 +0000|" + identityCommitMessage
	if authorship != want {
		t.Fatalf("identity commit metadata = %q, want %q", authorship, want)
	}
}

// TestIdentityCommitConvergesForIndependentAdopters is the property that makes
// publication safe without coordination: two clones that adopt the same
// identity build the same object, so the second publication is a no-op instead
// of a rejected competing history. It runs per object format because Workbook
// must not depend on a particular hash, and the expected ID is derived from the
// first clone rather than written down.
func TestIdentityCommitConvergesForIndependentAdopters(t *testing.T) {
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			testrepo.RequireObjectFormat(t, objectFormat)
			ctx := context.Background()
			tracked := core.ProjectConfig{
				Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
			}

			heads := make([]string, 2)
			for clone := range heads {
				repoDir := testrepo.New(t, testrepo.WithObjectFormat(objectFormat))
				// Deliberately different committer identities: the identity
				// commit must not inherit them.
				syncGit(t, repoDir, "config", "user.name", fmt.Sprintf("Adopter %d", clone))
				syncGit(t, repoDir, "config", "user.email", fmt.Sprintf("adopter%d@example.test", clone))
				writeProjectConfigFile(t, filepath.Join(repoDir, configPath), tracked)

				repo, err := Open(ctx, repoDir)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.LoadConfig(); err != nil {
					t.Fatalf("LoadConfig() error = %v", err)
				}
				heads[clone] = refValue(t, repo, identityRef)
			}
			if heads[0] != heads[1] {
				t.Fatalf("independent adopters published %q and %q, want one converged commit", heads[0], heads[1])
			}
			if objectFormat == testrepo.FormatSHA256 && len(heads[0]) != 64 {
				t.Fatalf("SHA-256 identity commit = %q, want a 64 character object ID", heads[0])
			}
			if objectFormat == testrepo.FormatSHA1 && len(heads[0]) != 40 {
				t.Fatalf("SHA-1 identity commit = %q, want a 40 character object ID", heads[0])
			}
		})
	}
}

// TestLoadIdentityPrecedence walks all four links of the chain.
func TestLoadIdentityPrecedence(t *testing.T) {
	ctx := context.Background()
	tracked := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
	}

	t.Run("ref wins", func(t *testing.T) {
		repoDir := testrepo.New(t)
		publishIdentityRefByHand(t, repoDir, fixedProjectID, "WB")
		writeProjectConfigFile(t, filepath.Join(repoDir, configPath), tracked)
		repo, err := Open(ctx, repoDir)
		if err != nil {
			t.Fatal(err)
		}

		identity, err := repo.LoadIdentity(ctx)
		if err != nil {
			t.Fatalf("LoadIdentity() error = %v", err)
		}
		if identity.ProjectID != fixedProjectID {
			t.Fatalf("LoadIdentity() = %#v, want the ref's project", identity)
		}
		origin, known := repo.IdentityOrigin()
		if !known || origin.Source != "ref" || origin.Published {
			t.Fatalf("IdentityOrigin() = (%#v, %t), want the ref with nothing published", origin, known)
		}
	})

	t.Run("tracked file is adopted and published", func(t *testing.T) {
		repoDir := testrepo.New(t)
		writeProjectConfigFile(t, filepath.Join(repoDir, configPath), tracked)
		repo, err := Open(ctx, repoDir)
		if err != nil {
			t.Fatal(err)
		}

		identity, err := repo.LoadIdentity(ctx)
		if err != nil {
			t.Fatalf("LoadIdentity() error = %v", err)
		}
		if identity.ProjectID != fixedProjectID {
			t.Fatalf("LoadIdentity() = %#v, want the tracked file's project", identity)
		}
		if !refExists(t, repo, identityRef) {
			t.Fatal("adopting the tracked identity did not publish the ref")
		}
		origin, _ := repo.IdentityOrigin()
		if origin.Source != "file" || !origin.Published || origin.Minted {
			t.Fatalf("IdentityOrigin() = %#v, want an adopted and published file identity", origin)
		}
		// The migration must leave the tracked document exactly as it found it:
		// a v0.4.x teammate reads those bytes.
		assertProjectConfigFile(t, filepath.Join(repoDir, configPath), tracked)
	})

	t.Run("private guard is adopted and published", func(t *testing.T) {
		repoDir := testrepo.New(t)
		repo, err := Open(ctx, repoDir)
		if err != nil {
			t.Fatal(err)
		}
		writeProjectConfigFile(t, filepath.Join(repo.CommonGitDir, "workbook", projectGuard), tracked)

		identity, err := repo.LoadIdentity(ctx)
		if err != nil {
			t.Fatalf("LoadIdentity() error = %v", err)
		}
		if identity.ProjectID != fixedProjectID {
			t.Fatalf("LoadIdentity() = %#v, want the guard's project", identity)
		}
		origin, _ := repo.IdentityOrigin()
		if origin.Source != "guard" || !origin.Published {
			t.Fatalf("IdentityOrigin() = %#v, want an adopted and published guard identity", origin)
		}
	})

	t.Run("nothing reports an uninitialized repository", func(t *testing.T) {
		repo, err := Open(ctx, testrepo.New(t))
		if err != nil {
			t.Fatal(err)
		}

		_, err = repo.LoadIdentity(ctx)
		if got, want := core.CategoryOf(err), core.CategoryNotInitialized; got != want {
			t.Fatalf("LoadIdentity() category = %q, want %q; error = %v", got, want, err)
		}
		if refExists(t, repo, identityRef) {
			t.Fatal("LoadIdentity() minted an identity; only Init may mint")
		}
	})
}

// TestInterruptedIdentityPublicationLeavesNoHalfState covers a process killed
// between writing the identity objects and moving the ref. Unreferenced objects
// are not state, so the retry must simply succeed — and, because the commit is
// deterministic, write the very same object.
func TestInterruptedIdentityPublicationLeavesNoHalfState(t *testing.T) {
	ctx := context.Background()
	repoDir := testrepo.New(t)
	repo, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	identity := core.ProjectIdentity{
		Format:    core.ProjectIdentityFormat,
		Version:   core.ProjectIdentityVersion,
		ProjectID: fixedProjectID,
		Key:       "WB",
	}

	interrupted, err := repo.writeIdentityObjects(ctx, identity)
	if err != nil {
		t.Fatalf("writeIdentityObjects() error = %v", err)
	}
	if refExists(t, repo, identityRef) {
		t.Fatal("writing identity objects moved a ref")
	}
	if _, err := repo.LoadConfig(); core.CategoryOf(err) != core.CategoryNotInitialized {
		t.Fatalf("LoadConfig() after an interrupted publication error = %v, want not-initialized", err)
	}

	retry, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	config, created, err := retry.Init(ctx, "WB", fixedIDs())
	if err != nil || !created {
		t.Fatalf("Init() retry = (_, %t, %v), want a completed publication", created, err)
	}
	if config.ProjectID != fixedProjectID {
		t.Fatalf("Init() retry project = %q, want %q", config.ProjectID, fixedProjectID)
	}
	if got := refValue(t, retry, identityRef); got != interrupted {
		t.Fatalf("retry published %q, want the same deterministic commit %q", got, interrupted)
	}
}

// TestReadIdentityRefRejectsMalformedRefs covers every structural rule a reader
// enforces on a ref a collaborator can push.
func TestReadIdentityRefRejectsMalformedRefs(t *testing.T) {
	ctx := context.Background()
	for name, corrupt := range map[string]func(t *testing.T, repoDir string){
		"commit with a parent": func(t *testing.T, repoDir string) {
			base := writeIdentityCommit(t, repoDir, fixedProjectID, "WB")
			tree := syncGit(t, repoDir, "rev-parse", base+"^{tree}")
			child := syncGit(t, repoDir, "commit-tree", tree, "-p", base, "-m", "second identity")
			syncGit(t, repoDir, "update-ref", identityRef, child)
		},
		"tree with a second entry": func(t *testing.T, repoDir string) {
			blob := syncGitInput(t, repoDir, identityDocumentBytes(fixedProjectID, "WB"), "hash-object", "-w", "--stdin")
			extra := syncGitInput(t, repoDir, []byte("extra\n"), "hash-object", "-w", "--stdin")
			entries := fmt.Sprintf("100644 blob %s\t%s\n100644 blob %s\textra.json\n", blob, identityDocumentPath, extra)
			tree := syncGitInput(t, repoDir, []byte(entries), "mktree")
			commit := syncGit(t, repoDir, "commit-tree", tree, "-m", "two documents")
			syncGit(t, repoDir, "update-ref", identityRef, commit)
		},
		"document that is not canonical": func(t *testing.T, repoDir string) {
			pretty := []byte("{\n  \"format\": \"workbook.project-identity\",\n  \"version\": 1,\n  \"projectId\": \"" +
				fixedProjectID + "\",\n  \"key\": \"WB\"\n}\n")
			blob := syncGitInput(t, repoDir, pretty, "hash-object", "-w", "--stdin")
			tree := syncGitInput(t, repoDir, []byte(fmt.Sprintf("100644 blob %s\t%s\n", blob, identityDocumentPath)), "mktree")
			commit := syncGit(t, repoDir, "commit-tree", tree, "-m", "pretty document")
			syncGit(t, repoDir, "update-ref", identityRef, commit)
		},
		"ref pointing at a blob": func(t *testing.T, repoDir string) {
			blob := syncGitInput(t, repoDir, identityDocumentBytes(fixedProjectID, "WB"), "hash-object", "-w", "--stdin")
			syncGit(t, repoDir, "update-ref", identityRef, blob)
		},
		"symbolic ref": func(t *testing.T, repoDir string) {
			commit := writeIdentityCommit(t, repoDir, fixedProjectID, "WB")
			syncGit(t, repoDir, "update-ref", identityRef+"-real", commit)
			syncGit(t, repoDir, "symbolic-ref", identityRef, identityRef+"-real")
		},
		"child ref under the identity name": func(t *testing.T, repoDir string) {
			commit := writeIdentityCommit(t, repoDir, fixedProjectID, "WB")
			syncGit(t, repoDir, "update-ref", identityRef+"/second", commit)
		},
	} {
		t.Run(name, func(t *testing.T) {
			repoDir := testrepo.New(t)
			corrupt(t, repoDir)
			repo, err := Open(ctx, repoDir)
			if err != nil {
				t.Fatal(err)
			}

			_, err = repo.LoadConfig()
			if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
				t.Fatalf("LoadConfig() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

// The tracked configuration keeps deciding the mutable preferences it owns,
// and an identity ref publication must not disturb a byte of it. Both document
// versions a project can hold are covered, because a v1 document predates the
// policy field entirely.
func TestLoadConfigKeepsTrackedPreferencesAfterAdoption(t *testing.T) {
	ctx := context.Background()
	for name, tracked := range map[string]core.ProjectConfig{
		"version 1": {
			Format: projectFormat, Version: legacyProjectVersion, ProjectID: fixedProjectID, Key: "WB",
		},
		"version 2": {
			Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
		},
		"version 2 with a policy": {
			Format: projectFormat, Version: projectVersion, ProjectID: fixedProjectID, Key: "WB",
			AutoSync: core.AutoSyncDisabled,
		},
	} {
		t.Run(name, func(t *testing.T) {
			repoDir := testrepo.New(t)
			configFile := filepath.Join(repoDir, configPath)
			writeProjectConfigFile(t, configFile, tracked)
			before, err := os.ReadFile(configFile)
			if err != nil {
				t.Fatal(err)
			}
			repo, err := Open(ctx, repoDir)
			if err != nil {
				t.Fatal(err)
			}

			got, err := repo.LoadConfig()
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got != tracked {
				t.Fatalf("LoadConfig() = %#v, want the tracked document unchanged %#v", got, tracked)
			}
			after, err := os.ReadFile(configFile)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("adoption rewrote the tracked document: before %q, after %q", before, after)
			}
		})
	}
}

// TestAdoptOriginProjectAdoptsOriginIdentityRefWithoutAnyCommittedConfig is the
// headline bootstrap case: origin publishes the identity ref, no branch in the
// repository carries .workbook/config.json, and setup must still join the
// existing project instead of minting a second one.
func TestAdoptOriginProjectAdoptsOriginIdentityRefWithoutAnyCommittedConfig(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "origin.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)

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
	commit := publishIdentityRefByHand(t, seedPath, fixedProjectID, "WB")
	syncGit(t, seedPath, "push", "--quiet", "origin", identityRef+":"+identityRef)

	clone := openSyncClone(t, bare)
	adopted, found, err := clone.AdoptOriginProject(ctx, "WB")
	if err != nil {
		t.Fatalf("AdoptOriginProject() error = %v", err)
	}
	if !found {
		t.Fatal("AdoptOriginProject() found = false, want adoption of origin's identity ref")
	}
	if adopted.ProjectID != fixedProjectID || adopted.Key != "WB" {
		t.Fatalf("AdoptOriginProject() = %#v, want project %s key WB", adopted, fixedProjectID)
	}
	if got := refValue(t, clone, identityRef); got != commit {
		t.Fatalf("local identity ref = %q, want origin's %q", got, commit)
	}

	config, created, err := clone.Init(ctx, "WB", mintingForbidden(t))
	if err != nil {
		t.Fatalf("Init() after ref adoption error = %v", err)
	}
	if created {
		t.Fatal("Init() created = true, want adoption of origin's published identity")
	}
	if config.ProjectID != fixedProjectID {
		t.Fatalf("Init() project = %q, want %q", config.ProjectID, fixedProjectID)
	}
}

// TestLoadConfigRepairsDisagreeingProjectGuardFromIdentityRef pins the v0.5.0
// role of the private guard: the ref is canonical, so a guard naming another
// project is repaired rather than a wedge that no command can get past.
func TestLoadConfigRepairsDisagreeingProjectGuardFromIdentityRef(t *testing.T) {
	ctx := context.Background()
	repoDir := testrepo.New(t)
	repo, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := repo.Init(ctx, "WB", fixedIDs())
	if err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(repo.CommonGitDir, "workbook", projectGuard)
	stale := want
	stale.ProjectID = foreignProjectID
	writeProjectConfigFile(t, guardPath, stale)

	fresh, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fresh.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() with a stale guard error = %v, want a repaired guard", err)
	}
	if got != want {
		t.Fatalf("LoadConfig() = %#v, want %#v", got, want)
	}
	repaired, exists, err := fresh.readProjectGuard()
	if err != nil || !exists {
		t.Fatalf("readProjectGuard() = (_, %t, %v), want the repaired guard", exists, err)
	}
	if !repaired.SameIdentity(want) {
		t.Fatalf("repaired guard = %#v, want the identity %#v", repaired, want)
	}
}

// TestLoadConfigRejectsTrackedConfigDisagreeingWithIdentityRef is the one
// identity disagreement that stays fatal, so the message has to carry every
// source and the way out.
func TestLoadConfigRejectsTrackedConfigDisagreeingWithIdentityRef(t *testing.T) {
	ctx := context.Background()
	repoDir := testrepo.New(t)
	repo, err := Open(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	tracked := core.ProjectConfig{
		Format: projectFormat, Version: projectVersion, ProjectID: foreignProjectID, Key: "WB",
	}
	writeProjectConfigFile(t, filepath.Join(repoDir, configPath), tracked)
	publishIdentityRefByHand(t, repoDir, fixedProjectID, "WB")

	_, err = repo.LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want a fatal identity mismatch")
	}
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("LoadConfig() category = %q, want %q; error = %v", got, want, err)
	}
	guardPath := filepath.Join(repo.CommonGitDir, "workbook", projectGuard)
	for _, want := range []string{
		identityRef,
		filepath.Join(repoDir, configPath),
		guardPath,
		fixedProjectID,
		foreignProjectID,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("LoadConfig() error %q does not name %q", err, want)
		}
	}
}

// TestSyncPublishesTheIdentityRefOnceAndThenAgrees covers the migration a
// project performs exactly once, and what every run after it reports.
func TestSyncPublishesTheIdentityRefOnceAndThenAgrees(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)

	run, err := first.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, run)
	}
	if run.Identity == nil || run.Identity.Status != SyncIdentityPublished {
		t.Fatalf("Sync() identity = %#v, want a publication", run.Identity)
	}
	published := remoteRefValue(t, first, identityRef)
	if run.Identity.Head != published {
		t.Fatalf("reported head %q, want origin's %q", run.Identity.Head, published)
	}

	again, err := first.Sync(ctx, config)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if again.Identity != nil {
		t.Fatalf("second Sync() identity = %#v, want nothing new to report", again.Identity)
	}

	// The second clone adopted the same identity from the tracked file, so it
	// built the same commit and has nothing to publish.
	other, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("other clone Sync() error = %v", err)
	}
	if other.Identity == nil || other.Identity.Status != SyncIdentityCreated {
		t.Fatalf("other clone identity = %#v, want its local publication reported", other.Identity)
	}
	if got := refValue(t, second, identityRef); got != published {
		t.Fatalf("other clone identity head = %q, want the converged %q", got, published)
	}
}

// TestSyncAdoptsOriginIdentityCommitCarryingTheSameDocument covers the narrow
// exception: origin holds a different commit object for the same document, so
// this clone converges on origin's instead of being rejected forever.
func TestSyncAdoptsOriginIdentityCommitCarryingTheSameDocument(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}
	published := remoteRefValue(t, first, identityRef)

	// Rebuild the same document under a commit with different metadata, the
	// shape a clone written by another build could hold.
	document := identityDocumentBytes(config.ProjectID, config.Key)
	blob := syncGitInput(t, second.Root, document, "hash-object", "-w", "--stdin")
	tree := syncGitInput(t, second.Root, []byte(fmt.Sprintf("100644 blob %s\t%s\n", blob, identityDocumentPath)), "mktree")
	divergent := syncGit(t, second.Root, "commit-tree", tree, "-m", "locally shaped identity")
	syncGit(t, second.Root, "update-ref", identityRef, divergent)
	if divergent == published {
		t.Fatal("the fixture did not produce a different commit object")
	}

	run, err := second.Sync(ctx, config)
	if err != nil {
		t.Fatalf("Sync() error = %v; result = %#v", err, run)
	}
	if run.Identity == nil || run.Identity.Status != SyncIdentityAdopted {
		t.Fatalf("Sync() identity = %#v, want origin's commit adopted", run.Identity)
	}
	if got := refValue(t, second, identityRef); got != published {
		t.Fatalf("local identity ref = %q, want origin's %q", got, published)
	}
}

// TestSyncStopsBeforeTasksWhenOriginIsADifferentProject is the reason the stage
// runs first: task history must never be replayed across projects.
func TestSyncStopsBeforeTasksWhenOriginIsADifferentProject(t *testing.T) {
	ctx := context.Background()
	first, second, config := syncRepositories(t)
	task := createSyncTask(t, first, config, "Published before the swap")
	if _, err := first.Sync(ctx, config); err != nil {
		t.Fatal(err)
	}

	// Origin is replaced by a different project's identity, the shape of a
	// remote pointed at the wrong repository.
	foreign := writeIdentityCommit(t, first.Root, foreignProjectID, "WB")
	syncGit(t, first.Root, "push", "--force", "origin", foreign+":"+identityRef)

	run, err := second.Sync(ctx, config)
	if err == nil {
		t.Fatalf("Sync() error = nil, want a refusal; result = %#v", run)
	}
	if got, want := core.CategoryOf(err), core.CategoryCorruptData; got != want {
		t.Fatalf("Sync() category = %q, want %q; error = %v", got, want, err)
	}
	if run.Identity == nil || run.Identity.Status != SyncIdentityMismatched {
		t.Fatalf("Sync() identity = %#v, want a mismatch", run.Identity)
	}
	if run.Fetch.Status != SyncPhaseFailed {
		t.Fatalf("fetch status = %q, want failed", run.Fetch.Status)
	}
	if len(run.Fetch.Tasks) != 0 {
		t.Fatalf("fetch reported task work %#v, want none after an identity mismatch", run.Fetch.Tasks)
	}
	if run.Push.Status != SyncPhaseSkipped {
		t.Fatalf("push status = %q, want skipped", run.Push.Status)
	}
	if refExists(t, second, taskRefPrefix+task.ID) {
		t.Fatal("a mismatched identity still fetched task refs")
	}
	for _, want := range []string{config.ProjectID, foreignProjectID, "git remote -v"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Sync() error %q does not name %q", err, want)
		}
	}
}

// Fetch runs the same stage but never writes to origin: downloading is not the
// moment to publish.
func TestFetchRunsTheIdentityStageWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	first, _, config := syncRepositories(t)

	if _, err := first.Fetch(ctx, config); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if remoteRefExists(t, first, identityRef) {
		t.Fatal("Fetch() published the identity ref to origin")
	}
	if !refExists(t, first, identityRef) {
		t.Fatal("Fetch() left this clone without its canonical identity")
	}
}

// TestVersionZeroFourClientNeverSeesTheIdentityRef runs the exact refspec and
// ls-remote patterns v0.4.x issues against an origin that has published the
// new ref. A mixed-version team keeps working only if those patterns stay blind
// to it.
func TestVersionZeroFourClientNeverSeesTheIdentityRef(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "origin.git")
	syncGit(t, t.TempDir(), "init", "--bare", "--quiet", bare)
	seedPath := testrepo.New(t)
	syncGit(t, seedPath, "remote", "add", "origin", bare)
	publishIdentityRefByHand(t, seedPath, fixedProjectID, "WB")
	syncGit(t, seedPath, "push", "--quiet", "origin", identityRef+":"+identityRef)

	clone := openSyncClone(t, bare)
	// v0.4.4's fetch refspec, verbatim.
	legacyRefspec := "+" + taskRefPrefix + "*:" + remoteTaskRefPrefix + "*"
	syncGit(t, clone.Root, "fetch", "--no-tags", "--prune", "origin", legacyRefspec)
	if refExists(t, clone, identityRef) {
		t.Fatal("a v0.4.x fetch refspec created the identity ref")
	}
	if refExists(t, clone, remoteIdentityRef) {
		t.Fatal("a v0.4.x fetch refspec created the identity tracking ref")
	}
	// v0.4.4's two ls-remote patterns, verbatim.
	for _, listing := range []string{
		syncGit(t, clone.Root, "ls-remote", "--refs", "origin", taskRefPrefix+"*"),
		syncGit(t, clone.Root, "ls-remote", "origin", "HEAD", taskRefPrefix+"*"),
	} {
		if strings.Contains(listing, identityRef) {
			t.Fatalf("a v0.4.x ls-remote pattern listed the identity ref: %q", listing)
		}
	}
}
