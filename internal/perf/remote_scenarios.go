package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dgoings/workbook/internal/gitstore"
)

const (
	workbookResultFormat = "workbook.result"
	workbookErrorFormat  = "workbook.error"
	workbookJSONVersion  = 1
)

type remoteResultEnvelope struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Command  string          `json:"command"`
	Data     json.RawMessage `json:"data"`
	Warnings json.RawMessage `json:"warnings,omitempty"`
}

type remoteErrorEnvelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Error   struct {
		Category string `json:"category"`
		Message  string `json:"message"`
	} `json:"error"`
}

type remoteScenarioResult struct {
	Fetch gitstore.SyncResult
	Push  gitstore.SyncResult
}

type remoteScenarioDependencies struct {
	buildFixture   func(context.Context, string, FixtureSpec, RemoteTopology) (RemoteFixture, error)
	measureCommand func(context.Context, CommandSpec) CommandMeasurement
}

type remoteScenarioDefinition struct {
	name          string
	topology      RemoteTopology
	command       string
	expectFailure bool
	target        ScenarioTarget
}

var remoteScenarioDefinitions = []remoteScenarioDefinition{
	{name: "sync-fresh-checkout", topology: RemoteFreshCheckout, command: "fetch", target: ScenarioTarget{MaxMilliseconds: 5000, MaxGitProcesses: 20}},
	{name: "sync-initial-publication", topology: RemoteInitialPublication, command: "push", target: ScenarioTarget{MaxMilliseconds: 5000, MaxGitProcesses: 20}},
	{name: "sync-already-synchronized", topology: RemoteAlreadySynchronized, command: "sync", target: ScenarioTarget{MaxMilliseconds: 1000, MaxGitProcesses: 10}},
	{name: "sync-small-changed-ref-set", topology: RemoteSmallChangedRefSet, command: "sync", target: ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}},
	{name: "sync-divergent-tips", topology: RemoteDivergentTips, command: "sync", expectFailure: true, target: ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}},
	{name: "sync-malformed-local-tip", topology: RemoteMalformedLocalTip, command: "push", expectFailure: true, target: ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}},
	{name: "sync-malformed-remote-tip", topology: RemoteMalformedRemoteTip, command: "fetch", expectFailure: true, target: ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}},
}

// RunRemoteScenarios measures only the requested remote synchronization
// topologies and verifies their Workbook output and resulting refs.
func RunRemoteScenarios(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string) ([]ScenarioResult, error) {
	return runRemoteScenarios(ctx, spec, fixtureRoot, selected, remoteScenarioDependencies{
		buildFixture:   buildRemoteFixtureWithinTimeout,
		measureCommand: MeasureCommandOutput,
	})
}

func buildRemoteFixtureWithinTimeout(ctx context.Context, root string, spec FixtureSpec, topology RemoteTopology) (RemoteFixture, error) {
	return BuildRemoteFixture(ctx, root, spec, topology)
}

func runRemoteScenarios(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string, dependencies remoteScenarioDependencies) ([]ScenarioResult, error) {
	if spec.WorkbookBinary == "" {
		return nil, fmt.Errorf("workbook binary is required")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if dependencies.buildFixture == nil || dependencies.measureCommand == nil {
		return nil, fmt.Errorf("remote scenario dependencies are required")
	}
	definitions, err := selectRemoteScenarioDefinitions(selected)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create remote fixture root: %w", err)
	}

	results := make([]ScenarioResult, 0, len(definitions))
	for _, definition := range definitions {
		fixtureContext, cancel := context.WithTimeout(ctx, spec.CommandTimeout)
		fixture, err := dependencies.buildFixture(fixtureContext, filepath.Join(fixtureRoot, definition.name), spec.Fixture, definition.topology)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("build %s fixture: %w", definition.name, err)
		}

		measurement := dependencies.measureCommand(ctx, CommandSpec{
			Binary:    spec.WorkbookBinary,
			Args:      []string{definition.command, "--json"},
			Directory: fixture.LocalRoot,
			Timeout:   spec.CommandTimeout,
		})
		if !measurement.Sample.TimedOut {
			if err := verifyRemoteScenarioMeasurement(definition, fixture, measurement); err != nil {
				return nil, fmt.Errorf("verify %s: %w", definition.name, err)
			}
		}
		target := definition.target
		results = append(results, ScenarioResult{
			Name:    definition.name,
			Surface: "remote-sync",
			Target:  &target,
			Samples: []Sample{measurement.Sample},
		})
	}
	for index := range results {
		results[index].Summary = Summarize(results[index].Samples)
	}
	return results, nil
}

func selectRemoteScenarioDefinitions(selected []string) ([]remoteScenarioDefinition, error) {
	selectedSet := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		if _, duplicate := selectedSet[name]; duplicate {
			return nil, fmt.Errorf("duplicate remote scenario %q", name)
		}
		selectedSet[name] = struct{}{}
	}
	definitions := make([]remoteScenarioDefinition, 0, len(selected))
	for _, definition := range remoteScenarioDefinitions {
		if _, wanted := selectedSet[definition.name]; wanted {
			definitions = append(definitions, definition)
			delete(selectedSet, definition.name)
		}
	}
	if len(selectedSet) != 0 {
		for _, name := range selected {
			if _, unknown := selectedSet[name]; unknown {
				return nil, fmt.Errorf("unknown remote scenario %q", name)
			}
		}
	}
	return definitions, nil
}

func verifyRemoteScenarioMeasurement(definition remoteScenarioDefinition, fixture RemoteFixture, measurement CommandMeasurement) error {
	if measurement.Sample.ExitCode < 0 {
		return fmt.Errorf("command did not produce an exit code: %s", measurement.Sample.Error)
	}
	if definition.expectFailure {
		if measurement.Sample.ExitCode == 0 {
			return fmt.Errorf("command succeeded, want expected product failure")
		}
	} else if measurement.Sample.ExitCode != 0 {
		return fmt.Errorf("command exit code = %d: %s", measurement.Sample.ExitCode, measurement.Sample.Error)
	}

	result, err := decodeRemoteScenarioResult(measurement.Stdout, measurement.Stderr, definition.command, definition.expectFailure)
	if err != nil {
		return err
	}
	fetch, push := expectedRemoteTaskResults(definition.topology, fixture.TaskIDs)
	if err := requireRemoteTaskResults("fetch", result.Fetch.Tasks, fetch); err != nil {
		return err
	}
	if err := requireRemoteTaskResults("push", result.Push.Tasks, push); err != nil {
		return err
	}
	return requireRemoteScenarioRefs(definition.topology, fixture)
}

func expectedRemoteTaskResults(topology RemoteTopology, taskIDs []string) ([]gitstore.SyncTaskResult, []gitstore.SyncTaskResult) {
	all := func(status gitstore.SyncStatus) []gitstore.SyncTaskResult {
		results := make([]gitstore.SyncTaskResult, len(taskIDs))
		for index, taskID := range taskIDs {
			results[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: status}
		}
		return results
	}
	switch topology {
	case RemoteFreshCheckout:
		return all(gitstore.SyncCreated), nil
	case RemoteInitialPublication:
		return nil, all(gitstore.SyncPublished)
	case RemoteAlreadySynchronized:
		return all(gitstore.SyncUnchanged), all(gitstore.SyncUpToDate)
	case RemoteSmallChangedRefSet:
		fetch := make([]gitstore.SyncTaskResult, len(taskIDs))
		push := make([]gitstore.SyncTaskResult, len(taskIDs))
		for index, taskID := range taskIDs {
			if index < 5 {
				fetch[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncLocalAhead}
				push[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncPublished}
			} else {
				fetch[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncFastForwarded}
				push[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncUpToDate}
			}
		}
		return fetch, push
	case RemoteDivergentTips:
		fetch := all(gitstore.SyncUnchanged)
		fetch[0].Status = gitstore.SyncDiverged
		return fetch, nil
	case RemoteMalformedLocalTip:
		push := all(gitstore.SyncUpToDate)
		push[0].Status = gitstore.SyncInvalid
		return nil, push
	case RemoteMalformedRemoteTip:
		fetch := all(gitstore.SyncUnchanged)
		fetch[0].Status = gitstore.SyncInvalid
		return fetch, nil
	default:
		panic("unsupported remote topology")
	}
}

func requireRemoteScenarioRefs(topology RemoteTopology, fixture RemoteFixture) error {
	want, err := expectedRemoteScenarioRefs(topology, fixture)
	if err != nil {
		return err
	}
	canonical, err := fixtureRefMapForRoot(context.Background(), fixture.LocalRoot, "refs/workbook/tasks/")
	if err != nil {
		return fmt.Errorf("read post-command refs: %w", err)
	}
	tracking, err := fixtureRefMapForRoot(context.Background(), fixture.LocalRoot, "refs/workbook/remotes/origin/tasks/")
	if err != nil {
		return fmt.Errorf("read post-command tracking refs: %w", err)
	}
	remote, err := fixtureRemoteRefMapForRoot(context.Background(), fixture.OriginRoot)
	if err != nil {
		return fmt.Errorf("read post-command remote refs: %w", err)
	}
	if err := requireExactRemoteRefNamespace("canonical", canonical, want, func(refs ExpectedRefs) string { return refs.Canonical }); err != nil {
		return err
	}
	if err := requireExactRemoteRefNamespace("tracking", tracking, want, func(refs ExpectedRefs) string { return refs.Tracking }); err != nil {
		return err
	}
	if err := requireExactRemoteRefNamespace("remote", remote, want, func(refs ExpectedRefs) string { return refs.Remote }); err != nil {
		return err
	}
	return nil
}

func requireExactRemoteRefNamespace(name string, got map[string]string, want map[string]ExpectedRefs, selectRef func(ExpectedRefs) string) error {
	wanted := make(map[string]string, len(want))
	for taskID, refs := range want {
		if ref := selectRef(refs); ref != "" {
			wanted[taskID] = ref
		}
	}
	if len(got) != len(wanted) {
		return fmt.Errorf("%s task ref count = %d, want %d", name, len(got), len(wanted))
	}
	for taskID, expected := range wanted {
		if actual, found := got[taskID]; !found {
			return fmt.Errorf("%s refs missing task %s", name, taskID)
		} else if actual != expected {
			return fmt.Errorf("%s ref for %s = %q, want %q", name, taskID, actual, expected)
		}
	}
	return nil
}

func expectedRemoteScenarioRefs(topology RemoteTopology, fixture RemoteFixture) (map[string]ExpectedRefs, error) {
	want := make(map[string]ExpectedRefs, len(fixture.Expected))
	for taskID, refs := range fixture.Expected {
		want[taskID] = refs
	}
	switch topology {
	case RemoteFreshCheckout:
		for _, taskID := range fixture.TaskIDs {
			refs := want[taskID]
			refs.Canonical, refs.Tracking = refs.Remote, refs.Remote
			want[taskID] = refs
		}
	case RemoteInitialPublication:
		for _, taskID := range fixture.TaskIDs {
			refs := want[taskID]
			refs.Remote = refs.Canonical
			want[taskID] = refs
		}
	case RemoteAlreadySynchronized, RemoteSmallChangedRefSet:
		for index, taskID := range fixture.TaskIDs {
			refs := want[taskID]
			if topology == RemoteSmallChangedRefSet && index < 5 {
				refs.Remote = refs.Canonical
			}
			refs.Canonical, refs.Tracking = refs.Remote, refs.Remote
			want[taskID] = refs
		}
	case RemoteDivergentTips, RemoteMalformedLocalTip, RemoteMalformedRemoteTip:
	default:
		return nil, fmt.Errorf("unsupported remote topology %q", topology)
	}
	return want, nil
}

// decodeRemoteScenarioResult validates Workbook's versioned output envelopes
// and decodes the command-specific synchronization result.
func decodeRemoteScenarioResult(stdout, stderr []byte, command string, expectFailure bool) (remoteScenarioResult, error) {
	if command != "fetch" && command != "push" && command != "sync" {
		return remoteScenarioResult{}, fmt.Errorf("unsupported remote scenario command %q", command)
	}
	var envelope remoteResultEnvelope
	if err := decodeRemoteJSON(stdout, &envelope); err != nil {
		return remoteScenarioResult{}, fmt.Errorf("decode result envelope: %w", err)
	}
	if envelope.Format != workbookResultFormat {
		return remoteScenarioResult{}, fmt.Errorf("result format = %q, want %q", envelope.Format, workbookResultFormat)
	}
	if envelope.Version != workbookJSONVersion {
		return remoteScenarioResult{}, fmt.Errorf("result version = %d, want %d", envelope.Version, workbookJSONVersion)
	}
	if envelope.Command != command {
		return remoteScenarioResult{}, fmt.Errorf("result command = %q, want %q", envelope.Command, command)
	}

	var result remoteScenarioResult
	switch command {
	case "fetch":
		if err := decodeRemoteJSON(envelope.Data, &result.Fetch); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode fetch result: %w", err)
		}
	case "push":
		if err := decodeRemoteJSON(envelope.Data, &result.Push); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode push result: %w", err)
		}
	case "sync":
		var run gitstore.SyncRunResult
		if err := decodeRemoteJSON(envelope.Data, &run); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode sync result: %w", err)
		}
		result.Fetch = run.Fetch
		result.Push = run.Push
	}
	if _, err := sortedRemoteTaskPairs(result.Fetch.Tasks); err != nil {
		return remoteScenarioResult{}, fmt.Errorf("fetch results: %w", err)
	}
	if _, err := sortedRemoteTaskPairs(result.Push.Tasks); err != nil {
		return remoteScenarioResult{}, fmt.Errorf("push results: %w", err)
	}

	if expectFailure {
		var errorEnvelope remoteErrorEnvelope
		if err := decodeRemoteJSON(stderr, &errorEnvelope); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode error envelope: %w", err)
		}
		if errorEnvelope.Format != workbookErrorFormat {
			return remoteScenarioResult{}, fmt.Errorf("error format = %q, want %q", errorEnvelope.Format, workbookErrorFormat)
		}
		if errorEnvelope.Version != workbookJSONVersion {
			return remoteScenarioResult{}, fmt.Errorf("error version = %d, want %d", errorEnvelope.Version, workbookJSONVersion)
		}
		if errorEnvelope.Error.Category == "" || errorEnvelope.Error.Message == "" {
			return remoteScenarioResult{}, fmt.Errorf("error envelope is missing category or message")
		}
	} else if len(bytes.TrimSpace(stderr)) != 0 {
		return remoteScenarioResult{}, fmt.Errorf("successful command wrote unexpected stderr")
	}
	return result, nil
}

func decodeRemoteJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected second JSON document")
		}
		return err
	}
	return nil
}

// requireRemoteTaskResults compares the complete task/status result set.
func requireRemoteTaskResults(phase string, got, want []gitstore.SyncTaskResult) error {
	gotPairs, err := sortedRemoteTaskPairs(got)
	if err != nil {
		return fmt.Errorf("%s results: %w", phase, err)
	}
	wantPairs, err := sortedRemoteTaskPairs(want)
	if err != nil {
		return fmt.Errorf("%s expected results: %w", phase, err)
	}
	if len(gotPairs) < len(wantPairs) {
		return fmt.Errorf("%s results missing task result: got %v, want %v", phase, gotPairs, wantPairs)
	}
	if len(gotPairs) > len(wantPairs) {
		return fmt.Errorf("%s results contain unexpected task result: got %v, want %v", phase, gotPairs, wantPairs)
	}
	for index := range wantPairs {
		if gotPairs[index] != wantPairs[index] {
			return fmt.Errorf("%s results = %v, want %v", phase, gotPairs, wantPairs)
		}
	}
	return nil
}

type remoteTaskPair struct {
	TaskID string
	Status gitstore.SyncStatus
}

func sortedRemoteTaskPairs(results []gitstore.SyncTaskResult) ([]remoteTaskPair, error) {
	pairs := make([]remoteTaskPair, len(results))
	seen := make(map[string]struct{}, len(results))
	for index, result := range results {
		if result.TaskID == "" || result.Status == "" {
			return nil, fmt.Errorf("task result %d is missing taskId or status", index)
		}
		if _, duplicate := seen[result.TaskID]; duplicate {
			return nil, fmt.Errorf("duplicate task result %q", result.TaskID)
		}
		seen[result.TaskID] = struct{}{}
		pairs[index] = remoteTaskPair{TaskID: result.TaskID, Status: result.Status}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].TaskID < pairs[j].TaskID })
	return pairs, nil
}
