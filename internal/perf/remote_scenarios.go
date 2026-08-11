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

	"github.com/dgoings/workbook/internal/core"
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
	Warnings []core.Warning  `json:"warnings,omitempty"`
}

type remoteErrorEnvelope struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Error   struct {
		Category core.Category `json:"category"`
		Message  string        `json:"message"`
	} `json:"error"`
}

type remoteScenarioResult struct {
	Remote string
	Fetch  gitstore.SyncResult
	Push   gitstore.SyncResult
}

type remoteScenarioDependencies struct {
	buildFixture      func(context.Context, string, FixtureSpec, RemoteTopology) (RemoteFixture, error)
	measureCommand    func(context.Context, CommandSpec) CommandMeasurement
	readCanonicalRefs func(context.Context, string) (map[string]string, error)
	readTrackingRefs  func(context.Context, string) (map[string]string, error)
	readRemoteRefs    func(context.Context, string) (map[string]string, error)
}

type remoteScenarioDefinition struct {
	name          string
	topology      RemoteTopology
	command       string
	expectFailure bool
	// target is nil for a scenario that is measured but has no approved
	// budget. Its samples are recorded and reported as not-evaluated rather
	// than classified against a threshold nobody has observed evidence for.
	target *ScenarioTarget
}

type remoteScenarioContract struct {
	command       string
	fetchStatus   gitstore.SyncPhaseStatus
	pushStatus    gitstore.SyncPhaseStatus
	expectFailure bool
	errorCategory core.Category
}

// The process budgets below carry a constant that arrived with the canonical
// project identity ref. Every command pays two Git processes to resolve it —
// one ref lookup and one object read — and every command that also talks to
// origin pays one more ref lookup to compare this clone with it. The cost does
// not grow with the number of tasks, the steady state reads no identity object
// at all because equal object IDs are equal documents, and what it buys is the
// guarantee that no clone ever replays another project's history. Each budget
// therefore rose by that constant and kept the headroom it had.
var remoteScenarioDefinitions = []remoteScenarioDefinition{
	{name: "sync-fresh-checkout", topology: RemoteFreshCheckout, command: "fetch", target: &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 5000, MaxGitProcesses: 23}},
	{name: "sync-initial-publication", topology: RemoteInitialPublication, command: "push", target: &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 5000, MaxGitProcesses: 22}},
	{name: "sync-already-synchronized", topology: RemoteAlreadySynchronized, command: "sync", target: &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 1000, MaxGitProcesses: 13}},
	{name: "sync-small-changed-ref-set", topology: RemoteSmallChangedRefSet, command: "sync", target: &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 2000, MaxGitProcesses: 23}},
	// Reconciliation replays local history rather than refusing to publish it,
	// so this scenario now measures work the earlier contract never did. It
	// stays unbudgeted until a recorded run says what that work costs.
	{name: "sync-divergent-tips", topology: RemoteDivergentTips, command: "sync"},
	{name: "sync-malformed-local-tip", topology: RemoteMalformedLocalTip, command: "push", expectFailure: true, target: &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 2000, MaxGitProcesses: 22}},
	{name: "sync-malformed-remote-tip", topology: RemoteMalformedRemoteTip, command: "fetch", expectFailure: true, target: &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 2000, MaxGitProcesses: 23}},
}

// RunRemoteScenarios measures only the requested remote synchronization
// topologies and verifies their Workbook output and resulting refs.
func RunRemoteScenarios(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string) ([]ScenarioResult, error) {
	return runRemoteScenarios(ctx, spec, fixtureRoot, selected, remoteScenarioDependencies{
		buildFixture:      buildRemoteFixtureWithinTimeout,
		measureCommand:    MeasureCommandOutput,
		readCanonicalRefs: readRemoteCanonicalRefs,
		readTrackingRefs:  readRemoteTrackingRefs,
		readRemoteRefs:    readRemoteOriginRefs,
	})
}

func buildRemoteFixtureWithinTimeout(ctx context.Context, root string, spec FixtureSpec, topology RemoteTopology) (RemoteFixture, error) {
	return BuildRemoteFixture(ctx, root, spec, topology)
}

func runRemoteScenarios(ctx context.Context, spec RunSpec, fixtureRoot string, selected []string, dependencies remoteScenarioDependencies) ([]ScenarioResult, error) {
	if spec.WorkbookBinary == "" {
		return nil, fmt.Errorf("workbook binary is required")
	}
	if spec.Samples < 1 {
		return nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, fmt.Errorf("command timeout must be positive")
	}
	if fixtureRoot == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if dependencies.readCanonicalRefs == nil {
		dependencies.readCanonicalRefs = readRemoteCanonicalRefs
	}
	if dependencies.readTrackingRefs == nil {
		dependencies.readTrackingRefs = readRemoteTrackingRefs
	}
	if dependencies.readRemoteRefs == nil {
		dependencies.readRemoteRefs = readRemoteOriginRefs
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
		result := ScenarioResult{
			Name:    definition.name,
			Surface: "remote-sync",
			Target:  definition.target,
			Samples: make([]Sample, spec.Samples),
		}
		for sample := range spec.Samples {
			fixtureContext, cancel := context.WithTimeout(ctx, spec.CommandTimeout)
			fixture, err := dependencies.buildFixture(fixtureContext, filepath.Join(fixtureRoot, definition.name, fmt.Sprintf("sample-%03d", sample+1)), spec.Fixture, definition.topology)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("build %s sample %d fixture: %w", definition.name, sample+1, err)
			}

			measurement := dependencies.measureCommand(ctx, CommandSpec{
				Binary:    spec.WorkbookBinary,
				Args:      []string{definition.command, "--json"},
				Directory: fixture.LocalRoot,
				Timeout:   spec.CommandTimeout,
			})
			if !measurement.Sample.TimedOut {
				if err := verifyRemoteScenarioMeasurement(ctx, definition, fixture, measurement, dependencies); err != nil {
					return nil, fmt.Errorf("verify %s sample %d: %w", definition.name, sample+1, err)
				}
			}
			result.Samples[sample] = measurement.Sample
		}
		results = append(results, result)
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

func verifyRemoteScenarioMeasurement(ctx context.Context, definition remoteScenarioDefinition, fixture RemoteFixture, measurement CommandMeasurement, dependencies remoteScenarioDependencies) error {
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

	result, err := decodeRemoteScenarioResultWithContract(measurement.Stdout, measurement.Stderr, remoteScenarioContractFor(definition))
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
	return requireRemoteScenarioRefs(ctx, definition.topology, fixture, dependencies)
}

func remoteScenarioContractFor(definition remoteScenarioDefinition) remoteScenarioContract {
	contract := remoteScenarioContract{
		command:       definition.command,
		fetchStatus:   gitstore.SyncPhaseCompleted,
		pushStatus:    gitstore.SyncPhaseCompleted,
		expectFailure: definition.expectFailure,
	}
	if definition.command == "fetch" {
		contract.pushStatus = ""
	}
	if definition.command == "push" {
		contract.fetchStatus = ""
	}
	if definition.topology == RemoteMalformedLocalTip || definition.topology == RemoteMalformedRemoteTip {
		contract.errorCategory = core.CategoryCorruptData
	}
	return contract
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
			switch {
			case index < 5:
				fetch[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncLocalAhead}
				push[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncPublished}
			case index < 10:
				fetch[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncFastForwarded}
				push[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncUpToDate}
			default:
				fetch[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncUnchanged}
				push[index] = gitstore.SyncTaskResult{TaskID: taskID, Status: gitstore.SyncUpToDate}
			}
		}
		return fetch, push
	case RemoteDivergentTips:
		fetch := all(gitstore.SyncUnchanged)
		fetch[0].Status = gitstore.SyncReconciled
		push := all(gitstore.SyncUpToDate)
		push[0].Status = gitstore.SyncPublished
		return fetch, push
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

func requireRemoteScenarioRefs(ctx context.Context, topology RemoteTopology, fixture RemoteFixture, dependencies remoteScenarioDependencies) error {
	want, err := expectedRemoteScenarioRefs(topology, fixture)
	if err != nil {
		return err
	}
	canonical, err := dependencies.readCanonicalRefs(ctx, fixture.LocalRoot)
	if err != nil {
		return fmt.Errorf("read post-command refs: %w", err)
	}
	tracking, err := dependencies.readTrackingRefs(ctx, fixture.LocalRoot)
	if err != nil {
		return fmt.Errorf("read post-command tracking refs: %w", err)
	}
	remote, err := dependencies.readRemoteRefs(ctx, fixture.OriginRoot)
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
	return requireReconciledRefsAgree(want, fixture, canonical, remote)
}

// requireReconciledRefsAgree checks the refs whose value a replay decides. The
// replayed commit ID cannot be predicted, so the assertion is that the canonical
// and published tips agree with each other and differ from both tips the
// fixture diverged from.
func requireReconciledRefsAgree(want map[string]ExpectedRefs, fixture RemoteFixture, canonical, remote map[string]string) error {
	for taskID, refs := range want {
		if refs.Canonical != remoteRefReconciled {
			continue
		}
		if canonical[taskID] != remote[taskID] {
			return fmt.Errorf("reconciled task %s canonical ref = %q, want the published %q", taskID, canonical[taskID], remote[taskID])
		}
		started := fixture.Expected[taskID]
		if canonical[taskID] == started.Canonical || canonical[taskID] == started.Remote {
			return fmt.Errorf("reconciled task %s ref = %q, want a replayed commit", taskID, canonical[taskID])
		}
	}
	return nil
}

func readRemoteCanonicalRefs(ctx context.Context, root string) (map[string]string, error) {
	return fixtureRefMapForRoot(ctx, root, "refs/workbook/tasks/")
}

func readRemoteTrackingRefs(ctx context.Context, root string) (map[string]string, error) {
	return fixtureRefMapForRoot(ctx, root, "refs/workbook/remotes/origin/tasks/")
}

func readRemoteOriginRefs(ctx context.Context, root string) (map[string]string, error) {
	return fixtureRemoteRefMapForRoot(ctx, root)
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
		actual, found := got[taskID]
		if !found {
			return fmt.Errorf("%s refs missing task %s", name, taskID)
		}
		if expected == remoteRefReconciled {
			continue
		}
		if actual != expected {
			return fmt.Errorf("%s ref for %s = %q, want %q", name, taskID, actual, expected)
		}
	}
	return nil
}

// remoteRefReconciled marks an expected ref whose value a replay produces. The
// commit ID depends on content the harness does not compute, so presence and
// cross-namespace agreement are asserted instead of an exact value.
const remoteRefReconciled = "reconciled"

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
	case RemoteAlreadySynchronized:
		for _, taskID := range fixture.TaskIDs {
			refs := want[taskID]
			refs.Canonical, refs.Tracking = refs.Remote, refs.Remote
			want[taskID] = refs
		}
	case RemoteSmallChangedRefSet:
		for index, taskID := range fixture.TaskIDs {
			refs := want[taskID]
			if index < 5 {
				refs.Remote = refs.Canonical
				refs.Canonical = refs.Remote
			} else {
				refs.Canonical, refs.Tracking = refs.Remote, refs.Remote
			}
			want[taskID] = refs
		}
	case RemoteDivergentTips:
		// Every unchanged task fast-forwards nothing and republishes nothing.
		// The divergent task's local operation is replayed onto origin's tip and
		// published, leaving tracking on the tip the fetch downloaded.
		for index, taskID := range fixture.TaskIDs {
			refs := want[taskID]
			if index == 0 {
				refs.Tracking = refs.Remote
				refs.Canonical, refs.Remote = remoteRefReconciled, remoteRefReconciled
			} else {
				refs.Canonical, refs.Tracking = refs.Remote, refs.Remote
			}
			want[taskID] = refs
		}
	case RemoteMalformedLocalTip, RemoteMalformedRemoteTip:
	default:
		return nil, fmt.Errorf("unsupported remote topology %q", topology)
	}
	return want, nil
}

// decodeRemoteScenarioResult validates Workbook's versioned output envelopes
// and decodes the command-specific synchronization result.
func decodeRemoteScenarioResult(stdout, stderr []byte, command string, expectFailure bool) (remoteScenarioResult, error) {
	contract := remoteScenarioContract{
		command:       command,
		fetchStatus:   gitstore.SyncPhaseCompleted,
		pushStatus:    gitstore.SyncPhaseCompleted,
		expectFailure: expectFailure,
	}
	if command == "fetch" {
		contract.pushStatus = ""
	}
	if command == "push" {
		contract.fetchStatus = ""
	}
	return decodeRemoteScenarioResultWithContract(stdout, stderr, contract)
}

func decodeRemoteScenarioResultWithContract(stdout, stderr []byte, contract remoteScenarioContract) (remoteScenarioResult, error) {
	if contract.command != "fetch" && contract.command != "push" && contract.command != "sync" {
		return remoteScenarioResult{}, fmt.Errorf("unsupported remote scenario command %q", contract.command)
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
	if envelope.Command != contract.command {
		return remoteScenarioResult{}, fmt.Errorf("result command = %q, want %q", envelope.Command, contract.command)
	}
	for index, warning := range envelope.Warnings {
		if warning.Code == "" || warning.Message == "" {
			return remoteScenarioResult{}, fmt.Errorf("warning %d is missing code or message", index)
		}
	}

	var result remoteScenarioResult
	switch contract.command {
	case "fetch":
		if err := decodeRemoteJSON(envelope.Data, &result.Fetch); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode fetch result: %w", err)
		}
		result.Remote = result.Fetch.Remote
	case "push":
		if err := decodeRemoteJSON(envelope.Data, &result.Push); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode push result: %w", err)
		}
		result.Remote = result.Push.Remote
	case "sync":
		var run gitstore.SyncRunResult
		if err := decodeRemoteJSON(envelope.Data, &run); err != nil {
			return remoteScenarioResult{}, fmt.Errorf("decode sync result: %w", err)
		}
		result.Remote = run.Remote
		result.Fetch = run.Fetch
		result.Push = run.Push
	}
	if result.Remote != "origin" {
		return remoteScenarioResult{}, fmt.Errorf("%s remote = %q, want origin", contract.command, result.Remote)
	}
	if err := requireRemoteSyncContract("fetch", result.Fetch, contract.fetchStatus); err != nil {
		return remoteScenarioResult{}, err
	}
	if err := requireRemoteSyncContract("push", result.Push, contract.pushStatus); err != nil {
		return remoteScenarioResult{}, err
	}
	if _, err := sortedRemoteTaskPairs(result.Fetch.Tasks); err != nil {
		return remoteScenarioResult{}, fmt.Errorf("fetch results: %w", err)
	}
	if _, err := sortedRemoteTaskPairs(result.Push.Tasks); err != nil {
		return remoteScenarioResult{}, fmt.Errorf("push results: %w", err)
	}

	if contract.expectFailure {
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
		if contract.errorCategory != "" && errorEnvelope.Error.Category != contract.errorCategory {
			return remoteScenarioResult{}, fmt.Errorf("error category = %q, want %q", errorEnvelope.Error.Category, contract.errorCategory)
		}
	} else if len(bytes.TrimSpace(stderr)) != 0 {
		return remoteScenarioResult{}, fmt.Errorf("successful command wrote unexpected stderr")
	}
	return result, nil
}

func requireRemoteSyncContract(phase string, result gitstore.SyncResult, wantStatus gitstore.SyncPhaseStatus) error {
	if wantStatus == "" {
		return nil
	}
	if result.Remote != "origin" {
		return fmt.Errorf("%s remote = %q, want origin", phase, result.Remote)
	}
	if result.Status != wantStatus {
		return fmt.Errorf("%s status = %q, want %q", phase, result.Status, wantStatus)
	}
	return nil
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
