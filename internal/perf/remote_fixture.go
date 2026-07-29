package perf

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
)

// RemoteTopology identifies one deterministic pre-measurement remote state.
type RemoteTopology string

const (
	RemoteFreshCheckout              RemoteTopology = "fresh-checkout"
	RemoteInitialPublication         RemoteTopology = "initial-publication"
	RemoteAlreadySynchronized        RemoteTopology = "already-synchronized"
	RemoteSmallChangedRefSet         RemoteTopology = "small-changed-ref-set"
	RemoteDivergentTips              RemoteTopology = "divergent-tips"
	RemoteMalformedLocalTip          RemoteTopology = "malformed-local-tip"
	RemoteMalformedRemoteTip         RemoteTopology = "malformed-remote-tip"
	RemoteBuriedCheckpointCorruption RemoteTopology = "buried-checkpoint-corruption"
)

// ExpectedRefs records a task's initial ref tips in the three namespaces used
// by the remote benchmark scenarios. An empty value means that namespace does
// not yet contain the task ref.
type ExpectedRefs struct {
	Canonical string
	Tracking  string
	Remote    string
}

// RemoteFixture is an independent local, peer, and bare-origin topology.
type RemoteFixture struct {
	LocalRoot  string
	PeerRoot   string
	OriginRoot string
	Config     core.ProjectConfig
	TaskIDs    []string
	Expected   map[string]ExpectedRefs
}

// BuildRemoteFixture constructs one isolated remote topology without invoking
// Workbook synchronization. Setup uses explicit Git refspecs and private
// fixture-only ref updates so the measured product behavior remains untouched.
func BuildRemoteFixture(ctx context.Context, root string, spec FixtureSpec, topology RemoteTopology) (RemoteFixture, error) {
	if !validRemoteTopology(topology) {
		return RemoteFixture{}, fmt.Errorf("unsupported remote topology %q", topology)
	}
	if spec.ActiveTasks < 10 && topology == RemoteSmallChangedRefSet {
		return RemoteFixture{}, fmt.Errorf("remote topology %q requires at least 10 active tasks", topology)
	}

	source, err := BuildFixture(ctx, filepath.Join(root, "source"), spec)
	if err != nil {
		return RemoteFixture{}, err
	}
	if err := prepareFixtureCodeBranch(ctx, source.Root); err != nil {
		return RemoteFixture{}, err
	}
	taskIDs := append([]string(nil), source.TaskIDs...)
	sort.Strings(taskIDs)

	originRoot := filepath.Join(root, "origin.git")
	if err := runFixtureGit(ctx, "init", "--bare", "--quiet", "--object-format="+spec.ObjectFormat, originRoot); err != nil {
		return RemoteFixture{}, err
	}
	localRoot, err := cloneFixtureRepository(ctx, source.Root, filepath.Join(root, "local"), originRoot)
	if err != nil {
		return RemoteFixture{}, err
	}
	peerRoot, err := cloneFixtureRepository(ctx, source.Root, filepath.Join(root, "peer"), originRoot)
	if err != nil {
		return RemoteFixture{}, err
	}

	if err := constructRemoteTopology(ctx, source.Root, localRoot, peerRoot, originRoot, source.Config, taskIDs, topology); err != nil {
		return RemoteFixture{}, err
	}
	expected, err := collectExpectedRefs(ctx, localRoot, originRoot, taskIDs)
	if err != nil {
		return RemoteFixture{}, err
	}
	return RemoteFixture{
		LocalRoot:  localRoot,
		PeerRoot:   peerRoot,
		OriginRoot: originRoot,
		Config:     source.Config,
		TaskIDs:    taskIDs,
		Expected:   expected,
	}, nil
}

func validRemoteTopology(topology RemoteTopology) bool {
	switch topology {
	case RemoteFreshCheckout, RemoteInitialPublication, RemoteAlreadySynchronized, RemoteSmallChangedRefSet, RemoteDivergentTips, RemoteMalformedLocalTip, RemoteMalformedRemoteTip, RemoteBuriedCheckpointCorruption:
		return true
	default:
		return false
	}
}

func prepareFixtureCodeBranch(ctx context.Context, root string) error {
	if err := runFixtureGit(ctx, "-C", root, "checkout", "--quiet", "-b", "workbook-benchmark"); err != nil {
		return err
	}
	if err := runFixtureGit(ctx, "-C", root, "add", ".workbook/config.json"); err != nil {
		return err
	}
	return runFixtureGit(ctx, "-C", root, "commit", "--quiet", "-m", "workbook: benchmark fixture configuration")
}

func cloneFixtureRepository(ctx context.Context, sourceRoot, destination, originRoot string) (string, error) {
	if err := runFixtureGit(ctx, "clone", "--quiet", "--origin", "seed", sourceRoot, destination); err != nil {
		return "", err
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve fixture clone: %w", err)
	}
	if err := runFixtureGit(ctx, "-C", absDestination, "remote", "add", "origin", originRoot); err != nil {
		return "", err
	}
	if err := runFixtureGit(ctx, "-C", absDestination, "config", "user.name", "Workbook Benchmark"); err != nil {
		return "", err
	}
	if err := runFixtureGit(ctx, "-C", absDestination, "config", "user.email", benchmarkActorID); err != nil {
		return "", err
	}
	return absDestination, nil
}

func constructRemoteTopology(ctx context.Context, sourceRoot, localRoot, peerRoot, originRoot string, config core.ProjectConfig, taskIDs []string, topology RemoteTopology) error {
	switch topology {
	case RemoteFreshCheckout:
		return publishFixtureTasks(ctx, sourceRoot, originRoot)
	case RemoteInitialPublication:
		return copyFixtureTasks(ctx, sourceRoot, localRoot)
	case RemoteAlreadySynchronized:
		if err := copyFixtureTasks(ctx, sourceRoot, localRoot); err != nil {
			return err
		}
		if err := publishFixtureTasks(ctx, localRoot, originRoot); err != nil {
			return err
		}
		return fetchFixtureTracking(ctx, localRoot)
	case RemoteSmallChangedRefSet:
		if err := populateSynchronizedFixture(ctx, sourceRoot, localRoot, peerRoot, originRoot); err != nil {
			return err
		}
		for taskIndex, taskID := range taskIDs[:5] {
			if err := appendFixtureTask(ctx, localRoot, config, taskID, taskIndex); err != nil {
				return err
			}
		}
		for taskIndex, taskID := range taskIDs[5:10] {
			if err := appendFixtureTask(ctx, peerRoot, config, taskID, taskIndex+5); err != nil {
				return err
			}
		}
		if err := publishFixtureTasks(ctx, peerRoot, originRoot); err != nil {
			return err
		}
		return fetchFixtureTracking(ctx, localRoot)
	case RemoteDivergentTips:
		if err := populateSynchronizedFixture(ctx, sourceRoot, localRoot, peerRoot, originRoot); err != nil {
			return err
		}
		if err := appendFixtureTask(ctx, localRoot, config, taskIDs[0], 0); err != nil {
			return err
		}
		if err := appendFixtureTask(ctx, peerRoot, config, taskIDs[0], 1000); err != nil {
			return err
		}
		if err := publishFixtureTasks(ctx, peerRoot, originRoot); err != nil {
			return err
		}
		return fetchFixtureTracking(ctx, localRoot)
	case RemoteMalformedLocalTip:
		if err := populateSynchronizedFixture(ctx, sourceRoot, localRoot, peerRoot, originRoot); err != nil {
			return err
		}
		return replaceFixtureRefWithMalformedCommit(ctx, localRoot, taskRefName(taskIDs[0]))
	case RemoteMalformedRemoteTip:
		if err := populateSynchronizedFixture(ctx, sourceRoot, localRoot, peerRoot, originRoot); err != nil {
			return err
		}
		if err := replaceFixtureRefWithMalformedCommit(ctx, peerRoot, taskRefName(taskIDs[0])); err != nil {
			return err
		}
		if err := publishFixtureTasks(ctx, peerRoot, originRoot); err != nil {
			return err
		}
		return fetchFixtureTracking(ctx, localRoot)
	case RemoteBuriedCheckpointCorruption:
		if err := populateSynchronizedFixture(ctx, sourceRoot, localRoot, peerRoot, originRoot); err != nil {
			return err
		}
		return writeBuriedCheckpointCorruption(ctx, localRoot, config, taskRefName(taskIDs[0]))
	default:
		return fmt.Errorf("unsupported remote topology %q", topology)
	}
}

func populateSynchronizedFixture(ctx context.Context, sourceRoot, localRoot, peerRoot, originRoot string) error {
	if err := copyFixtureTasks(ctx, sourceRoot, localRoot); err != nil {
		return err
	}
	if err := publishFixtureTasks(ctx, localRoot, originRoot); err != nil {
		return err
	}
	if err := fetchFixtureTracking(ctx, localRoot); err != nil {
		return err
	}
	return copyFixtureTasks(ctx, originRoot, peerRoot)
}

func copyFixtureTasks(ctx context.Context, from, to string) error {
	return runFixtureGit(ctx, "-C", to, "fetch", "--quiet", from, "refs/workbook/tasks/*:refs/workbook/tasks/*")
}

func publishFixtureTasks(ctx context.Context, from, originRoot string) error {
	return runFixtureGit(ctx, "-C", from, "push", "--quiet", originRoot, "refs/workbook/tasks/*:refs/workbook/tasks/*")
}

func fetchFixtureTracking(ctx context.Context, root string) error {
	return runFixtureGit(ctx, "-C", root, "fetch", "--quiet", "origin", "refs/workbook/tasks/*:refs/workbook/remotes/origin/tasks/*")
}

func appendFixtureTask(ctx context.Context, root string, config core.ProjectConfig, taskID string, taskIndex int) error {
	head, err := fixtureRefObjectID(ctx, root, taskRefName(taskID))
	if err != nil {
		return err
	}
	parent, err := readFixtureCommit(ctx, root, head)
	if err != nil {
		return err
	}
	ids := newFixtureIDs()
	ids.nextAt = benchmarkOrigin.AddDate(1, 0, 0).Add(time.Duration(taskIndex) * time.Millisecond)
	commit, err := appendFixtureOperation(ctx, root, config, parent, taskID, parent.Pack.HistoryGeneration, taskIndex, int(parent.Pack.LogicalClock)+1, ids)
	if err != nil {
		return err
	}
	return updateFixtureRef(ctx, root, taskRefName(taskID), commit.Head, head)
}

func replaceFixtureRefWithMalformedCommit(ctx context.Context, root, ref string) error {
	head, err := fixtureRefObjectID(ctx, root, ref)
	if err != nil {
		return err
	}
	tree, err := fixtureObjectID(ctx, root, nil, "mktree")
	if err != nil {
		return fmt.Errorf("write malformed fixture tree: %w", err)
	}
	commit, err := fixtureObjectID(ctx, root, nil, "commit-tree", tree, "-p", head, "-m", "workbook: malformed benchmark fixture")
	if err != nil {
		return fmt.Errorf("write malformed fixture commit: %w", err)
	}
	return updateFixtureRef(ctx, root, ref, commit, head)
}

func writeBuriedCheckpointCorruption(ctx context.Context, root string, config core.ProjectConfig, ref string) error {
	head, err := fixtureRefObjectID(ctx, root, ref)
	if err != nil {
		return err
	}
	validChild, err := readFixtureCommit(ctx, root, head)
	if err != nil {
		return err
	}
	previousHead, err := fixtureRevision(ctx, root, head+"^")
	if err != nil {
		return err
	}
	previous, err := readFixtureCommit(ctx, root, previousHead)
	if err != nil {
		return err
	}
	grandparent, err := fixtureRevision(ctx, root, previousHead+"^")
	if err != nil {
		return err
	}
	corruptState := previous.State
	corruptState.Task.Description = "Benchmark fixture mismatched checkpoint"
	corrupt, err := writeFixtureCommit(ctx, root, grandparent, previous.Pack, corruptState, "workbook: buried checkpoint corruption")
	if err != nil {
		return err
	}
	derivedState, err := core.Apply(&corrupt.State, validChild.Pack, config.Key)
	if err != nil {
		return fmt.Errorf("derive descendant from corrupt checkpoint: %w", err)
	}
	descendant, err := writeFixtureCommit(ctx, root, corrupt.Head, validChild.Pack, derivedState, "workbook: descendant of corrupt checkpoint")
	if err != nil {
		return err
	}
	return updateFixtureRef(ctx, root, ref, descendant.Head, head)
}

func taskRefName(taskID string) string {
	return "refs/workbook/tasks/" + taskID
}

func remoteTaskRef(taskID string) string {
	return "refs/workbook/remotes/origin/tasks/" + taskID
}

func updateFixtureRef(ctx context.Context, root, ref, next, previous string) error {
	if err := runFixtureGit(ctx, "-C", root, "check-ref-format", ref); err != nil {
		return fmt.Errorf("validate fixture ref %q: %w", ref, err)
	}
	resolved, err := fixtureRevision(ctx, root, next+"^{commit}")
	if err != nil {
		return fmt.Errorf("validate fixture commit %q: %w", next, err)
	}
	if resolved != next {
		return fmt.Errorf("fixture commit %q is not canonical", next)
	}
	return runFixtureGit(ctx, "-C", root, "update-ref", "--no-deref", ref, next, previous)
}

func fixtureRefObjectID(ctx context.Context, root, ref string) (string, error) {
	output, err := runFixtureGitOutput(ctx, root, nil, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return "", err
	}
	objectID, err := fixtureSingleLine(output)
	if err != nil {
		return "", fmt.Errorf("read fixture ref %q: %w", ref, err)
	}
	return objectID, nil
}

func fixtureRevision(ctx context.Context, root, revision string) (string, error) {
	output, err := runFixtureGitOutput(ctx, root, nil, "rev-parse", "--verify", revision)
	if err != nil {
		return "", err
	}
	return fixtureSingleLine(output)
}

func collectExpectedRefs(ctx context.Context, localRoot, originRoot string, taskIDs []string) (map[string]ExpectedRefs, error) {
	canonical, err := fixtureRefMapForRoot(ctx, localRoot, "refs/workbook/tasks/")
	if err != nil {
		return nil, err
	}
	tracking, err := fixtureRefMapForRoot(ctx, localRoot, "refs/workbook/remotes/origin/tasks/")
	if err != nil {
		return nil, err
	}
	remote, err := fixtureRemoteRefMapForRoot(ctx, originRoot)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]ExpectedRefs, len(taskIDs))
	for _, taskID := range taskIDs {
		expected[taskID] = ExpectedRefs{Canonical: canonical[taskID], Tracking: tracking[taskID], Remote: remote[taskID]}
	}
	return expected, nil
}

func fixtureRefMapForRoot(ctx context.Context, root, prefix string) (map[string]string, error) {
	output, err := runFixtureGitOutput(ctx, root, nil, "for-each-ref", "--format=%(refname)%00%(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid fixture ref record %q", line)
		}
		refs[strings.TrimPrefix(parts[0], prefix)] = parts[1]
	}
	return refs, nil
}

func fixtureRemoteRefMapForRoot(ctx context.Context, originRoot string) (map[string]string, error) {
	output, err := runFixtureGitOutput(ctx, originRoot, nil, "ls-remote", originRoot, "refs/workbook/tasks/*")
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid fixture remote ref record %q", line)
		}
		refs[strings.TrimPrefix(parts[1], "refs/workbook/tasks/")] = parts[0]
	}
	return refs, nil
}
