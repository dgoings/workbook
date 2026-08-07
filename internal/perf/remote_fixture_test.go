package perf

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/testenv"
)

func TestBuildRemoteFixture(t *testing.T) {
	formats := []string{"sha1", "sha256"}

	topologies := []RemoteTopology{
		RemoteFreshCheckout,
		RemoteInitialPublication,
		RemoteAlreadySynchronized,
		RemoteSmallChangedRefSet,
		RemoteDivergentTips,
		RemoteMalformedLocalTip,
		RemoteMalformedRemoteTip,
		RemoteBuriedCheckpointCorruption,
	}
	for _, objectFormat := range formats {
		t.Run(objectFormat, func(t *testing.T) {
			if objectFormat == "sha256" && !supportsRemoteObjectFormat(t, objectFormat) {
				testenv.MissingCapability(t, "Git does not support SHA-256 repositories")
			}
			for _, topology := range topologies {
				t.Run(string(topology), func(t *testing.T) {
					fixture, err := BuildRemoteFixture(context.Background(), filepath.Join(t.TempDir(), "fixture"), FixtureSpec{
						TotalTasks: 10, ActiveTasks: 10,
						OperationsPerTask: 4,
						ObjectFormat:      objectFormat,
					}, topology)
					if err != nil {
						t.Fatal(err)
					}
					if len(fixture.TaskIDs) != 10 {
						t.Fatalf("task IDs = %d, want 10", len(fixture.TaskIDs))
					}
					if !sort.StringsAreSorted(fixture.TaskIDs) {
						t.Fatalf("task IDs are not sorted: %v", fixture.TaskIDs)
					}
					assertRemoteFixtureRefs(t, fixture)

					switch topology {
					case RemoteFreshCheckout:
						if refs := fixtureRefMap(t, fixture.LocalRoot, "refs/workbook/tasks/"); len(refs) != 0 {
							t.Fatalf("fresh canonical refs = %v, want none", refs)
						}
						if refs := fixtureRefMap(t, fixture.LocalRoot, "refs/workbook/remotes/origin/tasks/"); len(refs) != 0 {
							t.Fatalf("fresh tracking refs = %v, want none", refs)
						}
						if refs := fixtureRemoteRefMap(t, fixture.OriginRoot); len(refs) != len(fixture.TaskIDs) {
							t.Fatalf("fresh remote refs = %d, want %d", len(refs), len(fixture.TaskIDs))
						}
					case RemoteInitialPublication:
						if refs := fixtureRemoteRefMap(t, fixture.OriginRoot); len(refs) != 0 {
							t.Fatalf("initial remote refs = %v, want none", refs)
						}
					case RemoteAlreadySynchronized:
						assertAllNamespacesMatch(t, fixture)
					case RemoteSmallChangedRefSet:
						assertChangedSet(t, fixture, 5, 5)
					case RemoteDivergentTips:
						assertDivergentChildren(t, fixture)
					case RemoteMalformedLocalTip:
						assertMalformedTree(t, fixture.LocalRoot, fixture.TaskIDs[0])
						expected := fixture.Expected[fixture.TaskIDs[0]]
						if expected.Tracking == "" || expected.Tracking != expected.Remote {
							t.Fatalf("malformed local remote/tracking refs = %#v", expected)
						}
						assertValidTaskTreeAtRef(t, fixture.LocalRoot, remoteTaskRef(fixture.TaskIDs[0]))
					case RemoteMalformedRemoteTip:
						assertMalformedTreeAtRef(t, fixture.LocalRoot, remoteTaskRef(fixture.TaskIDs[0]))
						expected := fixture.Expected[fixture.TaskIDs[0]]
						if expected.Canonical == expected.Tracking || expected.Tracking != expected.Remote {
							t.Fatalf("malformed tracking tip replaced canonical ref: %#v", expected)
						}
						assertValidTaskTree(t, fixture.LocalRoot, fixture.TaskIDs[0])
					case RemoteBuriedCheckpointCorruption:
						assertBuriedCorruption(t, fixture)
					}
				})
			}
		})
	}
}

// Mutation witness: indexing the active-task population without first
// validating it turns a valid all-tombstoned fixture into a setup panic.
func TestBuildRemoteFixtureRejectsTopologiesWithoutRequiredActiveTasks(t *testing.T) {
	spec := FixtureSpec{TotalTasks: 1, ActiveTasks: 0, TombstonedTasks: 1, OperationsPerTask: 2, ObjectFormat: "sha1"}
	fixture, err := BuildRemoteFixture(context.Background(), filepath.Join(t.TempDir(), "fresh"), spec, RemoteFreshCheckout)
	if err != nil {
		t.Fatalf("BuildRemoteFixture fresh all-tombstoned fixture: %v", err)
	}
	if got, want := len(fixture.TaskIDs), 1; got != want {
		t.Fatalf("fresh all-tombstoned task IDs = %d, want %d", got, want)
	}

	tests := []struct {
		topology RemoteTopology
		want     string
	}{
		{topology: RemoteSmallChangedRefSet, want: "requires at least 10 active tasks"},
		{topology: RemoteDivergentTips, want: "requires at least 1 active task"},
		{topology: RemoteMalformedLocalTip, want: "requires at least 1 active task"},
		{topology: RemoteMalformedRemoteTip, want: "requires at least 1 active task"},
		{topology: RemoteBuriedCheckpointCorruption, want: "requires at least 1 active task"},
	}
	for _, test := range tests {
		t.Run(string(test.topology), func(t *testing.T) {
			_, err := BuildRemoteFixture(context.Background(), filepath.Join(t.TempDir(), string(test.topology)), spec, test.topology)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildRemoteFixture(%s) error = %v, want %q", test.topology, err, test.want)
			}
		})
	}
}

func TestBuildRemoteFixtureUsesDeterministicSyntheticCommitIDs(t *testing.T) {
	topologies := []RemoteTopology{
		RemoteSmallChangedRefSet,
		RemoteDivergentTips,
		RemoteMalformedLocalTip,
		RemoteMalformedRemoteTip,
		RemoteBuriedCheckpointCorruption,
	}
	for _, topology := range topologies {
		t.Run(string(topology), func(t *testing.T) {
			spec := FixtureSpec{TotalTasks: 10, ActiveTasks: 10, OperationsPerTask: 4, ObjectFormat: "sha1"}
			first, err := BuildRemoteFixture(context.Background(), filepath.Join(t.TempDir(), "first"), spec, topology)
			if err != nil {
				t.Fatal(err)
			}
			second, err := BuildRemoteFixture(context.Background(), filepath.Join(t.TempDir(), "second"), spec, topology)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first.TaskIDs, second.TaskIDs) || !reflect.DeepEqual(first.Expected, second.Expected) {
				t.Fatalf("fixture IDs/refs differ:\nfirst=%#v\nsecond=%#v", first, second)
			}
		})
	}
}

func TestBuildRemoteFixtureIgnoresHostileGlobalSigningAndHooks(t *testing.T) {
	hostileRoot := t.TempDir()
	hooksRoot := filepath.Join(hostileRoot, "hooks")
	if err := os.Mkdir(hooksRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(hostileRoot, "hook-ran")
	hook := []byte("#!/bin/sh\nprintf hook-ran > '" + marker + "'\nexit 1\n")
	for _, name := range []string{"post-checkout", "pre-commit", "pre-push", "pre-receive"} {
		if err := os.WriteFile(filepath.Join(hooksRoot, name), hook, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	globalConfig := filepath.Join(hostileRoot, "gitconfig")
	if err := os.WriteFile(globalConfig, []byte(fmt.Sprintf("[commit]\n\tgpgSign = true\n[tag]\n\tgpgSign = true\n[push]\n\tgpgSign = true\n[core]\n\thooksPath = %s\n", hooksRoot)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	fixtureRoot := filepath.Join(t.TempDir(), "fixture")
	fixture, err := BuildRemoteFixture(context.Background(), fixtureRoot, FixtureSpec{
		TotalTasks: 10, ActiveTasks: 10,
		OperationsPerTask: 4,
		ObjectFormat:      "sha1",
	}, RemoteAlreadySynchronized)
	if err != nil {
		t.Fatalf("BuildRemoteFixture with hostile global Git configuration: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hostile Git hook ran: %v", err)
	}

	for _, root := range []string{filepath.Join(fixtureRoot, "source"), fixture.LocalRoot, fixture.PeerRoot, fixture.OriginRoot} {
		assertFixtureIsolationConfig(t, root)
	}
	identity := strings.TrimSpace(runGit(t, filepath.Join(fixtureRoot, "source"), "show", "-s", "--format=%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI", "workbook-benchmark"))
	wantIdentity := "Workbook Benchmark\x00" + benchmarkActorID + "\x002024-01-01T00:00:00Z\x00Workbook Benchmark\x00" + benchmarkActorID + "\x002024-01-01T00:00:00Z"
	if identity != wantIdentity {
		t.Fatalf("fixture code commit identity = %q, want %q", identity, wantIdentity)
	}
}

func assertFixtureIsolationConfig(t *testing.T, root string) {
	t.Helper()
	for key, want := range map[string]string{
		"commit.gpgSign": "false",
		"tag.gpgSign":    "false",
		"push.gpgSign":   "false",
	} {
		if got := strings.TrimSpace(runGit(t, root, "config", "--local", "--get", key)); got != want {
			t.Fatalf("%s local %s = %q, want %q", root, key, got, want)
		}
	}
	hooksPath := strings.TrimSpace(runGit(t, root, "config", "--local", "--get", "core.hooksPath"))
	if hooksPath == "" {
		t.Fatalf("%s local core.hooksPath is empty", root)
	}
	if _, err := os.Stat(hooksPath); !os.IsNotExist(err) {
		t.Fatalf("%s local core.hooksPath = %q must not exist: %v", root, hooksPath, err)
	}
}

func assertRemoteFixtureRefs(t *testing.T, fixture RemoteFixture) {
	t.Helper()
	canonical := fixtureRefMap(t, fixture.LocalRoot, "refs/workbook/tasks/")
	tracking := fixtureRefMap(t, fixture.LocalRoot, "refs/workbook/remotes/origin/tasks/")
	remote := fixtureRemoteRefMap(t, fixture.OriginRoot)
	for _, taskID := range fixture.TaskIDs {
		got, found := fixture.Expected[taskID]
		if !found {
			t.Fatalf("missing expected refs for %q", taskID)
		}
		if got.Canonical != canonical[taskID] || got.Tracking != tracking[taskID] || got.Remote != remote[taskID] {
			t.Fatalf("refs for %q = %#v, want %#v", taskID, ExpectedRefs{
				Canonical: canonical[taskID],
				Tracking:  tracking[taskID],
				Remote:    remote[taskID],
			}, got)
		}
	}
}

func fixtureRefMap(t *testing.T, root, prefix string) map[string]string {
	t.Helper()
	output := runGit(t, root, "for-each-ref", "--format=%(refname)%00%(objectname)", prefix)
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 2 {
			t.Fatalf("for-each-ref line = %q", line)
		}
		taskID := strings.TrimPrefix(parts[0], prefix)
		refs[taskID] = parts[1]
	}
	return refs
}

func fixtureRemoteRefMap(t *testing.T, origin string) map[string]string {
	t.Helper()
	output := runGit(t, origin, "ls-remote", origin, "refs/workbook/tasks/*")
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			t.Fatalf("ls-remote line = %q", line)
		}
		refs[strings.TrimPrefix(parts[1], "refs/workbook/tasks/")] = parts[0]
	}
	return refs
}

func assertAllNamespacesMatch(t *testing.T, fixture RemoteFixture) {
	t.Helper()
	for _, expected := range fixture.Expected {
		if expected.Canonical == "" || expected.Canonical != expected.Tracking || expected.Canonical != expected.Remote {
			t.Fatalf("expected synchronized refs = %#v", fixture.Expected)
		}
	}
}

func assertChangedSet(t *testing.T, fixture RemoteFixture, wantLocalAhead, wantRemoteAhead int) {
	t.Helper()
	var localAhead, remoteAhead int
	for _, expected := range fixture.Expected {
		if expected.Tracking != expected.Remote {
			t.Fatalf("changed-set tracking ref = %q, want remote ref %q", expected.Tracking, expected.Remote)
		}
		switch {
		case expected.Canonical != expected.Remote && gitIsAncestor(t, fixture.LocalRoot, expected.Remote, expected.Canonical):
			localAhead++
		case expected.Canonical != expected.Remote && gitIsAncestor(t, fixture.LocalRoot, expected.Canonical, expected.Remote):
			remoteAhead++
		}
	}
	if localAhead != wantLocalAhead || remoteAhead != wantRemoteAhead {
		t.Fatalf("changed refs local-ahead=%d remote-ahead=%d, want %d and %d", localAhead, remoteAhead, wantLocalAhead, wantRemoteAhead)
	}
}

func supportsRemoteObjectFormat(t *testing.T, objectFormat string) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	command := exec.Command("git", "init", "--object-format="+objectFormat, probe)
	output, err := command.CombinedOutput()
	if err == nil {
		return true
	}
	message := strings.ToLower(string(output))
	if strings.Contains(message, "not supported") || strings.Contains(message, "unknown hash algorithm") || strings.Contains(message, "invalid value for '--object-format'") {
		return false
	}
	t.Fatalf("git init --object-format=%s: %v\n%s", objectFormat, err, output)
	return false
}

func assertDivergentChildren(t *testing.T, fixture RemoteFixture) {
	t.Helper()
	expected := fixture.Expected[fixture.TaskIDs[0]]
	if expected.Canonical == expected.Remote || expected.Tracking != expected.Remote {
		t.Fatalf("divergent refs = %#v", expected)
	}
	localParent := strings.TrimSpace(runGit(t, fixture.LocalRoot, "rev-parse", expected.Canonical+"^"))
	remoteParent := strings.TrimSpace(runGit(t, fixture.LocalRoot, "rev-parse", expected.Remote+"^"))
	if localParent != remoteParent {
		t.Fatalf("divergent parents = %q and %q", localParent, remoteParent)
	}
}

func assertMalformedTree(t *testing.T, root, taskID string) {
	t.Helper()
	assertMalformedTreeAtRef(t, root, "refs/workbook/tasks/"+taskID)
}

func assertMalformedTreeAtRef(t *testing.T, root, ref string) {
	t.Helper()
	names := strings.Fields(runGit(t, root, "ls-tree", "--name-only", ref))
	if strings.Join(names, ",") == "operation.json,state.json" {
		t.Fatalf("%s has a valid task tree", ref)
	}
}

func assertValidTaskTree(t *testing.T, root, taskID string) {
	t.Helper()
	assertValidTaskTreeAtRef(t, root, "refs/workbook/tasks/"+taskID)
}

func assertValidTaskTreeAtRef(t *testing.T, root, ref string) {
	t.Helper()
	names := strings.Fields(runGit(t, root, "ls-tree", "--name-only", ref))
	if strings.Join(names, ",") != "operation.json,state.json" {
		t.Fatalf("%s task tree = %v, want operation.json and state.json", ref, names)
	}
}

func assertBuriedCorruption(t *testing.T, fixture RemoteFixture) {
	t.Helper()
	head := fixture.Expected[fixture.TaskIDs[0]].Canonical
	if head == "" {
		t.Fatal("buried corruption fixture has no local head")
	}
	if err := validateFixtureTip(t, fixture.LocalRoot, fixture.Config, fixture.TaskIDs[0]); err != nil {
		t.Fatalf("tip should remain structurally readable: %v", err)
	}
	commits := strings.Fields(runGit(t, fixture.LocalRoot, "rev-list", head))
	if len(commits) < 3 {
		t.Fatalf("history = %v, want a buried commit", commits)
	}
	if !checkpointMatchesParent(t, fixture.LocalRoot, fixture.Config, commits[0]) {
		t.Fatalf("tip %q checkpoint does not match its parent", commits[0])
	}
	if checkpointMatchesParent(t, fixture.LocalRoot, fixture.Config, commits[1]) {
		t.Fatalf("commit %q checkpoint unexpectedly matches its parent", commits[1])
	}
}

func gitIsAncestor(t *testing.T, root, ancestor, descendant string) bool {
	t.Helper()
	command := exec.Command("git", "-C", root, "merge-base", "--is-ancestor", ancestor, descendant)
	err := command.Run()
	if err == nil {
		return true
	}
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git merge-base --is-ancestor %q %q: %v", ancestor, descendant, err)
	return false
}

func validateFixtureTip(t *testing.T, root string, config core.ProjectConfig, taskID string) error {
	t.Helper()
	repository, err := gitstore.Open(context.Background(), root)
	if err != nil {
		return err
	}
	_, err = repository.Get(context.Background(), config, taskID)
	return err
}

func checkpointMatchesParent(t *testing.T, root string, config core.ProjectConfig, commit string) bool {
	t.Helper()
	parentState, err := core.DecodeStateDocument([]byte(runGit(t, root, "show", commit+"^:state.json")))
	if err != nil {
		t.Fatal(err)
	}
	operation, err := core.DecodeOperationPack([]byte(runGit(t, root, "show", commit+":operation.json")))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := core.DecodeStateDocument([]byte(runGit(t, root, "show", commit+":state.json")))
	if err != nil {
		t.Fatal(err)
	}
	return core.ValidateCheckpoint(&parentState, operation, stored, config.Key) == nil
}
