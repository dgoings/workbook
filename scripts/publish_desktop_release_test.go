package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The desktop app publishes two releases from one run: the versioned one, which
// is the record of what shipped, and desktop-latest, the rolling one the
// download links and the updater point at. Both have to end up holding the same
// build, and the rolling one must never move ahead of a versioned release that
// failed to publish.
func TestPublishDesktopReleaseCreatesTheVersionedReleaseThenTheRollingOne(t *testing.T) {
	clone, remote := newDesktopReleaseRepository(t, "desktop-v0.6.0")
	distribution := writeDesktopFixture(t, "", desktopAssetNames()...)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	releasedCommit := gitOutput(t, clone, "rev-parse", "HEAD")

	output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, nil,
		"desktop-v0.6.0", distribution, "dgoings/workbook", "--bundles", "v0.6.0",
	)
	if err != nil {
		t.Fatalf("publish desktop release: %v\n%s", err, output)
	}

	log := readFakeGitHubLog(t, fakeGitHub)
	createLine := fakeGitHubLine(t, log, "release create desktop-v0.6.0")
	// Production mutation: creating the release without --verify-tag would
	// publish a tag GitHub invents on the spot, naming a commit nobody built;
	// creating it without --draft would publish it before the assets landed.
	for _, want := range []string{"--verify-tag", "--draft", "--title Workbench desktop-v0.6.0"} {
		if !strings.Contains(createLine, want) {
			t.Errorf("create line = %q, want it to contain %q", createLine, want)
		}
	}
	// The CLI the build bundles is otherwise unanswerable from the page.
	if !strings.Contains(createLine, "bundling Workbook v0.6.0") {
		t.Errorf("create line = %q, want the bundled CLI named in the notes", createLine)
	}
	editLine := fakeGitHubLine(t, log, "release edit desktop-v0.6.0")
	if !strings.Contains(editLine, "--draft=false") {
		t.Errorf("edit line = %q, want the draft published", editLine)
	}
	// Production mutation: refreshing the rolling release before the versioned
	// one is published would point every download at a draft.
	if strings.Index(log, "release create desktop-latest") < strings.Index(log, "release edit desktop-v0.6.0") {
		t.Errorf("rolling release was refreshed before the versioned one was published:\n%s", log)
	}
	if !strings.Contains(log, "release create desktop-latest") {
		t.Errorf("gh log = %q, want the rolling release created on the first run", log)
	}
	// Production mutation: leaving the rolling tag where it was would serve the
	// previous build from a release page announcing this one.
	if got := gitOutput(t, remote, "rev-parse", "desktop-latest^{commit}"); got != releasedCommit {
		t.Errorf("remote desktop-latest points at %s, want the released commit %s", got, releasedCommit)
	}
	for _, name := range desktopAssetNames() {
		for _, release := range []string{"desktop-v0.6.0", "desktop-latest"} {
			if _, statErr := os.Stat(filepath.Join(fakeReleaseAssetsPath(fakeGitHub, release), name)); statErr != nil {
				t.Errorf("%s is missing from %s: %v", name, release, statErr)
			}
		}
	}
}

// A candidate has to be flagged as one in all four places it can be stated, or
// the updater and the releases page hand an rc to everyone as a finished build.
func TestPublishDesktopReleaseFlagsACandidateEverywhere(t *testing.T) {
	clone, _ := newDesktopReleaseRepository(t, "desktop-v0.6.0-rc1")
	distribution := writeDesktopFixture(t, "", desktopAssetNames()...)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, nil,
		"desktop-v0.6.0-rc1", distribution, "dgoings/workbook",
	)
	if err != nil {
		t.Fatalf("publish desktop pre-release: %v\n%s", err, output)
	}

	log := readFakeGitHubLog(t, fakeGitHub)
	for _, prefix := range []string{
		"release create desktop-v0.6.0-rc1",
		"release edit desktop-v0.6.0-rc1",
		"release create desktop-latest",
	} {
		if line := fakeGitHubLine(t, log, prefix); !strings.Contains(line, "--prerelease") {
			t.Errorf("%q line = %q, want the pre-release flag", prefix, line)
		}
	}
	// No --bundles, so the notes say only what the release is.
	createLine := fakeGitHubLine(t, log, "release create desktop-v0.6.0-rc1")
	if strings.Contains(createLine, "bundling") {
		t.Errorf("create line = %q, want no bundled CLI named without --bundles", createLine)
	}

	// The second run finds desktop-latest already there and has to patch it
	// rather than create it, restating the flag: gh only touches the flags it
	// is given.
	if output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, nil,
		"desktop-v0.6.0-rc1", distribution, "dgoings/workbook",
	); err != nil {
		t.Fatalf("republish desktop pre-release: %v\n%s", err, output)
	}
	log = readFakeGitHubLog(t, fakeGitHub)
	uploadLine := fakeGitHubLine(t, log, "release upload desktop-latest")
	if !strings.Contains(uploadLine, "--clobber") {
		t.Errorf("upload line = %q, want --clobber so the previous build's assets are replaced", uploadLine)
	}
	rollingEditLine := fakeGitHubLine(t, log, "release edit desktop-latest")
	if !strings.Contains(rollingEditLine, "--prerelease") {
		t.Errorf("rolling edit line = %q, want the pre-release flag restated", rollingEditLine)
	}
	if !strings.Contains(rollingEditLine, "Rolling release; currently desktop-v0.6.0-rc1.") {
		t.Errorf("rolling edit line = %q, want the notes to name the current release", rollingEditLine)
	}
}

// A stable release published after a candidate has to clear the flag, or the
// finished build stays hidden behind "pre-release" forever.
func TestPublishDesktopReleaseClearsTheCandidateFlagOnTheRollingRelease(t *testing.T) {
	clone, _ := newDesktopReleaseRepository(t, "desktop-v0.6.0-rc1")
	distribution := writeDesktopFixture(t, "", desktopAssetNames()...)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	if output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, nil,
		"desktop-v0.6.0-rc1", distribution, "dgoings/workbook",
	); err != nil {
		t.Fatalf("publish desktop pre-release: %v\n%s", err, output)
	}

	runCommand(t, clone, nil, "git", "tag", "--annotate", "desktop-v0.6.0", "--message", "Workbench desktop-v0.6.0")
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/desktop-v0.6.0")
	stable := writeDesktopFixture(t, "", desktopAssetNames()...)
	if output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, nil,
		"desktop-v0.6.0", stable, "dgoings/workbook",
	); err != nil {
		t.Fatalf("publish desktop release: %v\n%s", err, output)
	}

	log := readFakeGitHubLog(t, fakeGitHub)
	rollingEditLine := fakeGitHubLine(t, log, "release edit desktop-latest")
	if !strings.Contains(rollingEditLine, "--prerelease=false") {
		t.Errorf("rolling edit line = %q, want the pre-release flag cleared for a stable release", rollingEditLine)
	}
	if line := fakeGitHubLine(t, log, "release create desktop-v0.6.0 "); strings.Contains(line, "--prerelease") {
		t.Errorf("create line = %q, want no pre-release flag on a stable release", line)
	}
}

// The workflow can be rerun over the same artifacts, so an identical rerun has
// to be a no-op on the versioned release and still refresh the rolling one:
// re-creating the release would fail, and skipping the refresh would leave the
// rolling release behind after a transient failure part-way through.
func TestPublishDesktopReleaseRerunsWithIdenticalAssetsWithoutRecreatingTheRelease(t *testing.T) {
	clone, remote := newDesktopReleaseRepository(t, "desktop-v0.6.0")
	distribution := writeDesktopFixture(t, "", desktopAssetNames()...)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)
	releasedCommit := gitOutput(t, clone, "rev-parse", "HEAD")

	for run := 0; run < 2; run++ {
		if output, err := runPublishDesktopRelease(
			t, clone, fakeBin, fakeGitHub, nil,
			"desktop-v0.6.0", distribution, "dgoings/workbook",
		); err != nil {
			t.Fatalf("publish desktop release run %d: %v\n%s", run+1, err, output)
		}
	}

	log := readFakeGitHubLog(t, fakeGitHub)
	// Production mutation: creating the versioned release on every run would
	// fail the rerun, and replacing its assets would change what someone has
	// already downloaded.
	if count := strings.Count(log, "release create desktop-v"); count != 1 {
		t.Errorf("versioned release create count = %d, want exactly one across two runs; log:\n%s", count, log)
	}
	if !strings.Contains(log, "release upload desktop-latest") {
		t.Errorf("gh log = %q, want the rolling release refreshed on the rerun", log)
	}
	if got := gitOutput(t, remote, "rev-parse", "desktop-latest^{commit}"); got != releasedCommit {
		t.Errorf("remote desktop-latest points at %s, want the released commit %s", got, releasedCommit)
	}
}

// Assets that differ from the published ones mean the rerun is not the same
// build. Uploading them would rewrite a release people have downloaded, so the
// run stops before touching anything.
func TestPublishDesktopReleaseRefusesARerunWithChangedAssets(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		tamper      func(t *testing.T, distribution string)
		wantMessage string
	}{{
		name: "changed bytes",
		tamper: func(t *testing.T, distribution string) {
			writeDesktopFile(t, distribution, "Workbench-arm64.dmg", "a different build\n")
		},
		wantMessage: "does not match existing release asset",
	}, {
		name: "an asset the release does not have",
		tamper: func(t *testing.T, distribution string) {
			writeDesktopFile(t, distribution, "Workbench-extra.dmg", "an asset the release does not have\n")
		},
		wantMessage: "existing release is missing asset Workbench-extra.dmg",
	}, {
		name: "an asset this build does not have",
		tamper: func(t *testing.T, distribution string) {
			if err := os.Remove(filepath.Join(distribution, "latest-linux.yml")); err != nil {
				t.Fatalf("remove asset: %v", err)
			}
		},
		wantMessage: "existing release has unexpected asset latest-linux.yml",
	}} {
		t.Run(testCase.name, func(t *testing.T) {
			clone, _ := newDesktopReleaseRepository(t, "desktop-v0.6.0")
			distribution := writeDesktopFixture(t, "", desktopAssetNames()...)
			fakeBin, fakeGitHub := newFakeGitHubCLI(t)

			if output, err := runPublishDesktopRelease(
				t, clone, fakeBin, fakeGitHub, nil,
				"desktop-v0.6.0", distribution, "dgoings/workbook",
			); err != nil {
				t.Fatalf("publish desktop release: %v\n%s", err, output)
			}
			testCase.tamper(t, distribution)

			output, err := runPublishDesktopRelease(
				t, clone, fakeBin, fakeGitHub, nil,
				"desktop-v0.6.0", distribution, "dgoings/workbook",
			)
			if err == nil {
				t.Fatalf("mismatched rerun succeeded; output = %q", output)
			}
			if !strings.Contains(output, testCase.wantMessage) {
				t.Errorf("output = %q, want %q", output, testCase.wantMessage)
			}
			// Production mutation: a refusal that still refreshed the rolling
			// release would publish the very build it just rejected.
			if log := readFakeGitHubLog(t, fakeGitHub); strings.Contains(log, "release upload") {
				t.Errorf("mismatched rerun uploaded assets:\n%s", log)
			}
		})
	}
}

// Every platform's installer has to be in the build. A release missing one is a
// broken build, not a partial one, and finding that out after half the release
// is published leaves a page nobody can fix by rerunning.
func TestPublishDesktopReleaseFailsBeforeGitHubWhenAPlatformIsMissing(t *testing.T) {
	for _, missing := range []string{
		"Workbench-arm64.dmg",
		"Workbench-x64.dmg",
		"Workbench-x64.AppImage",
		"Workbench-amd64.deb",
		"Workbench-Setup-x64.exe",
	} {
		t.Run("without "+missing, func(t *testing.T) {
			clone, _ := newDesktopReleaseRepository(t, "desktop-v0.6.0")
			names := make([]string, 0, len(desktopAssetNames()))
			for _, name := range desktopAssetNames() {
				if name != missing {
					names = append(names, name)
				}
			}
			distribution := writeDesktopFixture(t, "", names...)
			fakeBin, fakeGitHub := newFakeGitHubCLI(t)

			output, err := runPublishDesktopRelease(
				t, clone, fakeBin, fakeGitHub, nil,
				"desktop-v0.6.0", distribution, "dgoings/workbook",
			)
			if err == nil {
				t.Fatalf("publisher accepted a build without %s; output = %q", missing, output)
			}
			if !strings.Contains(output, "missing release asset") {
				t.Errorf("output = %q, want a missing-asset error", output)
			}
			// Production mutation: checking the assets after the release is
			// created leaves a draft behind for a build that can never ship.
			if _, statErr := os.Stat(filepath.Join(fakeGitHub, "commands.log")); !os.IsNotExist(statErr) {
				t.Errorf("publisher reached gh before failing on the missing asset: %v", statErr)
			}
		})
	}
}

// electron-builder writes its own debug log into the distribution directory.
// It is build output, not something anyone downloads, and uploading it would
// also leave the rerun check comparing against an asset the next build renames.
func TestPublishDesktopReleaseDoesNotUploadTheBuilderDebugLog(t *testing.T) {
	clone, _ := newDesktopReleaseRepository(t, "desktop-v0.6.0")
	distribution := writeDesktopFixture(t, "", append(desktopAssetNames(), "builder-debug.yml")...)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	if output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, nil,
		"desktop-v0.6.0", distribution, "dgoings/workbook",
	); err != nil {
		t.Fatalf("publish desktop release: %v\n%s", err, output)
	}

	for _, release := range []string{"desktop-v0.6.0", "desktop-latest"} {
		if _, statErr := os.Stat(filepath.Join(fakeReleaseAssetsPath(fakeGitHub, release), "builder-debug.yml")); !os.IsNotExist(statErr) {
			t.Errorf("builder-debug.yml was uploaded to %s: %v", release, statErr)
		}
	}
	if log := readFakeGitHubLog(t, fakeGitHub); strings.Contains(log, "builder-debug.yml") {
		t.Errorf("gh log names the builder debug log:\n%s", log)
	}
	// The rest of the build still has to be there, so the exclusion is by name
	// rather than a rule that quietly drops the .yml update manifests too.
	if _, statErr := os.Stat(filepath.Join(fakeReleaseAssetsPath(fakeGitHub, "desktop-v0.6.0"), "latest-mac.yml")); statErr != nil {
		t.Errorf("latest-mac.yml was not uploaded: %v", statErr)
	}
}

// A run that cannot publish its own draft has published nothing, and it has to
// leave the world that way: a lingering draft blocks the rerun, and a rolling
// release refreshed from it would announce a release that does not exist.
func TestPublishDesktopReleaseRollsBackItsDraftWhenPublicationFails(t *testing.T) {
	clone, remote := newDesktopReleaseRepository(t, "desktop-v0.6.0")
	distribution := writeDesktopFixture(t, "", desktopAssetNames()...)
	fakeBin, fakeGitHub := newFakeGitHubCLI(t)

	output, err := runPublishDesktopRelease(
		t, clone, fakeBin, fakeGitHub, []string{"FAKE_GH_FAIL_PUBLISH=1"},
		"desktop-v0.6.0", distribution, "dgoings/workbook",
	)
	if err == nil {
		t.Fatalf("publisher succeeded despite a publication failure; output = %q", output)
	}

	log := readFakeGitHubLog(t, fakeGitHub)
	if !strings.Contains(log, "release delete desktop-v0.6.0 --repo dgoings/workbook --yes") {
		t.Errorf("publisher did not delete the draft it created:\n%s", log)
	}
	if _, statErr := os.Stat(fakeReleaseStatePath(fakeGitHub, "desktop-v0.6.0")); !os.IsNotExist(statErr) {
		t.Errorf("the new draft was not deleted during rollback: %v", statErr)
	}
	// Production mutation: refreshing the rolling release regardless of whether
	// the versioned one published would point every download at a release that
	// was just rolled back.
	if _, statErr := os.Stat(fakeReleaseStatePath(fakeGitHub, "desktop-latest")); !os.IsNotExist(statErr) {
		t.Errorf("the rolling release was created despite the publication failure: %v", statErr)
	}
	if tags := gitOutput(t, remote, "tag", "--list", "desktop-latest"); tags != "" {
		t.Errorf("remote rolling tag = %q, want it left alone after a failed publication", tags)
	}
}

// desktopAssetNames lists one installer per platform plus the update manifests
// and the side files electron-builder leaves beside them in dist/.
func desktopAssetNames() []string {
	return []string{
		"Workbench-arm64.dmg",
		"Workbench-x64.dmg",
		"Workbench-arm64.dmg.blockmap",
		"Workbench-x64.zip",
		"Workbench-x64.AppImage",
		"Workbench-amd64.deb",
		"Workbench-Setup-x64.exe",
		"latest-mac.yml",
		"latest-linux.yml",
		"latest.yml",
	}
}

// writeDesktopFixture fills a distribution directory with one small file per
// name, each holding its own name so a swapped or rewritten asset is visible
// byte for byte. An empty directory means a fresh temporary one.
func writeDesktopFixture(t *testing.T, directory string, names ...string) string {
	t.Helper()
	if directory == "" {
		directory = t.TempDir()
	}
	for _, name := range names {
		writeDesktopFile(t, directory, name, name+" fixture\n")
	}
	return directory
}

func writeDesktopFile(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newDesktopReleaseRepository builds the repository the workflow publishes
// from: a clone of a bare remote with the desktop tag already on HEAD, as the
// workflow's checkout of the pushed tag leaves it.
func newDesktopReleaseRepository(t *testing.T, tag string) (string, string) {
	t.Helper()
	clone, remote := newReleaseRepository(t)
	runCommand(t, clone, nil, "git", "tag", "--annotate", tag, "--message", "Workbench "+tag)
	runCommand(t, clone, nil, "git", "push", "--quiet", "origin", "refs/tags/"+tag)
	return clone, remote
}

// The script runs by absolute path so it finds its own siblings, with the
// fixture repository as its working directory so its git commands land there.
func runPublishDesktopRelease(
	t *testing.T,
	clone, fakeBin, fakeGitHub string,
	extraEnvironment []string,
	args ...string,
) (string, error) {
	t.Helper()
	environment := environmentWithValues(
		os.Environ(),
		append([]string{
			"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH"),
			"FAKE_GH_ROOT=" + fakeGitHub,
		}, extraEnvironment...)...,
	)
	return runReleaseScriptWithEnvironment(t, clone, "publish-desktop-release.sh", "", environment, args...)
}

// fakeGitHubLine returns the last logged gh invocation starting with prefix, so
// a test can assert on the flags of one command rather than on the whole log,
// where a flag another command carries would satisfy the check by accident.
func fakeGitHubLine(t *testing.T, log, prefix string) string {
	t.Helper()
	line := ""
	for _, candidate := range strings.Split(log, "\n") {
		if strings.HasPrefix(candidate, prefix) {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("no gh command starting with %q in log:\n%s", prefix, log)
	}
	return line
}
