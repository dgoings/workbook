package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// archiveFixture is two tasks ready to be archived: the first with a
// three-commit history and two parked refs, one of them at a sibling commit
// nothing else reaches, and the second with a single commit and no parked
// refs. manifest describes both at their current tips.
type archiveFixture struct {
	repo     *Repository
	config   core.ProjectConfig
	first    string
	second   string
	parked   []string
	sibling  string
	manifest ArchiveManifest
}

func newArchiveFixture(t *testing.T, repo *Repository, config core.ProjectConfig) archiveFixture {
	t.Helper()
	first := createSyncTask(t, repo, config, "Archive me")
	created := currentSnapshot(t, repo, config, first.ID)
	updateSyncTask(t, repo, config, first.ID, "Archive me, second title")
	second := currentSnapshot(t, repo, config, first.ID)
	updateSyncTask(t, repo, config, first.ID, "Archive me, third title")
	firstTip := currentSnapshot(t, repo, config, first.ID)

	sibling := syncGit(t, repo.Root, "commit-tree", second.Head+"^{tree}", "-p", created.Head, "-m", "parked sibling")
	parked := []string{
		reconciledRefPrefix + first.ID + "/0",
		reconciledRefPrefix + first.ID + "/1",
	}
	syncGit(t, repo.Root, "update-ref", parked[0], created.Head)
	syncGit(t, repo.Root, "update-ref", parked[1], sibling)

	other := createSyncTask(t, repo, config, "Archive me too")
	otherTip := currentSnapshot(t, repo, config, other.ID)

	manifest := ArchiveManifest{
		Format:    "workbook.archive-manifest",
		Version:   1,
		ProjectID: config.ProjectID,
		RunID:     newCheckpointID(t),
		CreatedAt: time.Date(2026, time.November, 13, 21, 4, 5, 0, time.UTC),
		Flags:     []string{"--status", "done", "--older-than", "30d"},
		Tasks: []ArchiveTask{
			{
				TaskID:        first.ID,
				Title:         firstTip.State.Task.Title,
				OldTip:        firstTip.Head,
				OldGeneration: firstTip.State.History.Generation,
				NewGeneration: newCheckpointID(t),
				Commits:       3,
			},
			{
				TaskID:        other.ID,
				Title:         otherTip.State.Task.Title,
				OldTip:        otherTip.Head,
				OldGeneration: otherTip.State.History.Generation,
				NewGeneration: newCheckpointID(t),
				Commits:       1,
			},
		},
	}
	return archiveFixture{
		repo: repo, config: config, first: first.ID, second: other.ID,
		parked: parked, sibling: sibling, manifest: manifest,
	}
}

func assertNoArchiveManifestRef(t *testing.T, repo *Repository) {
	t.Helper()
	if refExists(t, repo, archiveManifestRef) {
		t.Fatalf("%s survived in the source repository", archiveManifestRef)
	}
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(%s) error = %v, want it not to exist", path, err)
	}
}

func TestWriteArchiveBundleContainsTasksParkedRefsAndManifest(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	testWriteArchiveBundleContents(t, repo, config, testrepo.FormatSHA1)
}

func TestWriteArchiveBundleContainsTasksParkedRefsAndManifestSHA256(t *testing.T) {
	t.Parallel()
	repo, _, config := syncRepositoriesWithObjectFormat(t, testrepo.FormatSHA256)
	if got := syncGit(t, repo.Root, "rev-parse", "--show-object-format"); got != testrepo.FormatSHA256 {
		t.Fatalf("source object format = %q, want %q", got, testrepo.FormatSHA256)
	}
	testWriteArchiveBundleContents(t, repo, config, testrepo.FormatSHA256)
}

func testWriteArchiveBundleContents(t *testing.T, repo *Repository, config core.ProjectConfig, objectFormat string) {
	t.Helper()
	fixture := newArchiveFixture(t, repo, config)
	// The parent directory does not exist yet: the write creates it.
	path := filepath.Join(t.TempDir(), "nested", "archive", fixture.manifest.RunID+".bundle")

	if err := repo.WriteArchiveBundle(context.Background(), config, path, fixture.manifest); err != nil {
		t.Fatalf("WriteArchiveBundle() error = %v", err)
	}
	assertNoArchiveManifestRef(t, repo)
	assertNoFile(t, path+".tmp")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(bundle) error = %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o111 != 0 {
		t.Fatalf("bundle mode = %v, want it not executable", mode)
	}

	target := testrepo.New(t, testrepo.WithObjectFormat(objectFormat))
	syncGit(t, target, "fetch", "--quiet", path, "refs/*:refs/*")
	targetRepo := &Repository{Root: target}

	for _, task := range fixture.manifest.Tasks {
		if got := refValue(t, targetRepo, taskRefPrefix+task.TaskID); got != task.OldTip {
			t.Fatalf("bundle task %s = %s, want its old tip %s", task.TaskID, got, task.OldTip)
		}
	}
	for _, name := range fixture.parked {
		if got, want := refValue(t, targetRepo, name), refValue(t, repo, name); got != want {
			t.Fatalf("bundle parked ref %s = %s, want %s", name, got, want)
		}
	}
	if got := syncGit(t, target, "cat-file", "-t", fixture.sibling); got != "commit" {
		t.Fatalf("parked sibling %s in the bundle has type %q, want commit", fixture.sibling, got)
	}
	got := strings.Fields(syncGit(t, target, "for-each-ref", "--format=%(refname)"))
	want := []string{
		archiveManifestRef,
		fixture.parked[0],
		fixture.parked[1],
	}
	for _, task := range fixture.manifest.Tasks {
		want = append(want, taskRefPrefix+task.TaskID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("bundle refs = %v, want exactly %v", got, want)
	}

	// The manifest is a parentless commit whose tree holds manifest.json alone.
	if parents := strings.Fields(syncGit(t, target, "rev-list", "--parents", "-n", "1", archiveManifestRef)); len(parents) != 1 {
		t.Fatalf("manifest commit parents = %v, want none", parents[1:])
	}
	if names := syncGit(t, target, "ls-tree", "--name-only", archiveManifestRef); names != "manifest.json" {
		t.Fatalf("manifest tree = %q, want manifest.json alone", names)
	}
	var decoded ArchiveManifest
	if err := json.Unmarshal([]byte(syncGit(t, target, "show", archiveManifestRef+":manifest.json")), &decoded); err != nil {
		t.Fatalf("decode manifest.json: %v", err)
	}
	if !reflect.DeepEqual(decoded, fixture.manifest) {
		t.Fatalf("manifest.json = %+v, want %+v", decoded, fixture.manifest)
	}
	raw := syncGit(t, target, "show", archiveManifestRef+":manifest.json")
	for _, key := range []string{
		`"format":"workbook.archive-manifest"`, `"version":1`, `"projectId"`, `"runId"`, `"createdAt"`,
		`"flags"`, `"tasks"`, `"taskId"`, `"title"`, `"oldTip"`, `"oldGeneration"`, `"newGeneration"`, `"commits"`,
	} {
		if !strings.Contains(strings.Join(strings.Fields(raw), ""), key) {
			t.Fatalf("manifest.json lacks %s:\n%s", key, raw)
		}
	}
}

func TestWriteArchiveBundleRefusesExistingPath(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	fixture := newArchiveFixture(t, repo, config)
	path := filepath.Join(t.TempDir(), "existing.bundle")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := repo.WriteArchiveBundle(context.Background(), config, path, fixture.manifest)
	if err == nil {
		t.Fatalf("WriteArchiveBundle() over an existing file succeeded")
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "keep me\n" {
		t.Fatalf("existing file = %q, want it untouched", contents)
	}
	assertNoFile(t, path+".tmp")
	assertNoArchiveManifestRef(t, repo)
}

func TestWriteArchiveBundleDetectsMovedTask(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	fixture := newArchiveFixture(t, repo, config)
	// The run observed the task one commit earlier than it now stands.
	fixture.manifest.Tasks[0].OldTip = syncGit(t, repo.Root, "rev-parse", fixture.manifest.Tasks[0].OldTip+"^")
	path := filepath.Join(t.TempDir(), "moved.bundle")

	err := repo.WriteArchiveBundle(context.Background(), config, path, fixture.manifest)
	if got, want := core.CategoryOf(err), core.CategoryStaleWrite; got != want {
		t.Fatalf("WriteArchiveBundle(moved) category = %q, want %q; error = %v", got, want, err)
	}
	if !strings.Contains(err.Error(), fixture.first) {
		t.Fatalf("WriteArchiveBundle(moved) error = %v, want it to name task %s", err, fixture.first)
	}
	assertNoFile(t, path)
	assertNoFile(t, path+".tmp")
	assertNoArchiveManifestRef(t, repo)
}

// A failure while Git writes the bundle, after the manifest ref exists, still
// removes the ref and publishes nothing.
func TestWriteArchiveBundleCleansUpWhenBundlingFails(t *testing.T) {
	t.Parallel()
	repo, config := writeRepository(t)
	fixture := newArchiveFixture(t, repo, config)
	path := filepath.Join(t.TempDir(), "blocked.bundle")
	// Git cannot rename its finished bundle over a non-empty directory.
	if err := os.MkdirAll(filepath.Join(path+".tmp", "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := repo.WriteArchiveBundle(context.Background(), config, path, fixture.manifest)
	if err == nil {
		t.Fatalf("WriteArchiveBundle() with its temporary path blocked succeeded")
	}
	if !strings.Contains(err.Error(), "bundle create") {
		t.Fatalf("WriteArchiveBundle() error = %v, want it to fail creating the bundle", err)
	}
	assertNoFile(t, path)
	assertNoArchiveManifestRef(t, repo)
}

func TestResolveArchivePath(t *testing.T) {
	t.Parallel()
	common := t.TempDir()
	directory := t.TempDir()
	const runID = "01KA0000000000000000000000"
	defaultPath := filepath.Join(common, "workbook-archive", runID+".bundle")
	if got := DefaultArchivePath(common, runID); got != defaultPath {
		t.Fatalf("DefaultArchivePath() = %q, want %q", got, defaultPath)
	}

	existingFile := filepath.Join(directory, "taken.bundle")
	if err := os.WriteFile(existingFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, flag, want string
	}{
		{name: "default", flag: "", want: defaultPath},
		{name: "existing directory", flag: directory, want: filepath.Join(directory, runID+".bundle")},
		{name: "new file", flag: filepath.Join(directory, "missing", "mine.bundle"), want: filepath.Join(directory, "missing", "mine.bundle")},
		// An existing file is still a file path: the write refuses it later.
		{name: "existing file", flag: existingFile, want: existingFile},
	} {
		got, err := ResolveArchivePath(common, runID, test.flag)
		if err != nil {
			t.Fatalf("%s: ResolveArchivePath(%q) error = %v", test.name, test.flag, err)
		}
		if got != test.want {
			t.Fatalf("%s: ResolveArchivePath(%q) = %q, want %q", test.name, test.flag, got, test.want)
		}
	}
}
