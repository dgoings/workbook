package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
)

// The relic family. It is not in scenarioRegistry: its fixture takes minutes
// to write, so only workbook-bench --relics runs it.
var relicScenarioNames = []string{
	"relic-create-large",
	"relic-edit-part-large",
	"relic-show-large",
	"relic-move-part-large",
	"relic-create-16mib",
	"relic-show-16mib",
	"relic-edit-part-16mib",
	"relic-show-part-2kib",
	"relic-body-2kib",
	"relic-body-1mib",
	"relic-history-100",
	"relic-diff-latest-100kib",
	"relic-diff-first-last-100kib",
	"relic-edit-body-1mib",
	"relic-list",
	"relic-list-type",
	"relic-linked-to-hot",
	"relic-linked-to-typical",
	"task-show-linked-hot",
	"relic-linked-to-part-hot",
	"relic-show-linked-part-hot",
	"relic-push-100",
	"relic-fetch-100",
	"relic-refresh-0",
	"relic-refresh-1",
	"relic-refresh-100",
	"relic-sync-merge-100",
	"relic-merge-repetitive-1mib",
	"relic-clone-setup",
	"relic-rebuild-fresh-clone",
	"relic-first-read-fresh-clone",
	"relic-validate-full",
}

// RelicScenarioNames returns the relic family's scenarios in report order.
// The names are the story's, at every scale: at the smoke scale
// relic-body-1mib reads the smoke body, relic-create-16mib writes the smoke
// FullBodiesBytes, relic-push-100 pushes the smoke count, the history and
// diff scenarios read the smoke history relic and the merge scenarios merge the
// smoke merge set.
func RelicScenarioNames() []string {
	return append([]string(nil), relicScenarioNames...)
}

// RelicRunSpec is a relic benchmark run. RunSpec.Fixture is the task fixture
// the relics are added to, normally Relics.TaskFixture(objectFormat).
type RelicRunSpec struct {
	RunSpec
	Relics RelicFixtureSpec
}

// RelicTaskComparison is one cli-* scenario measured on the task fixture
// alone and on the same fixture with the relic set added.
type RelicTaskComparison struct {
	Name    string  `json:"name"`
	Without Summary `json:"without"`
	With    Summary `json:"with"`
}

// RelicBenchmarkReport is the descriptive account of a relic run, beside the
// scenarios it timed. It holds no budget and no verdict.
//
// GitCommands holds, per scenario, the highest count of each Git subcommand
// over its samples. StorageBefore is the main repository packed after the
// relics are created; StorageAfter is the same repository packed after the
// links, the moves and the small edits relic-edit-part-large and
// relic-move-part-large make there. The scenarios that create relics run in a
// copy of the fixture, so no created relic is in either figure, and after
// minus before is what the links and moves cost with those few edits. The
// syncs run in the clones, which hold no more than the main repository. The
// fixture writes its moves as MovesPerUpdate moves per commit, so the storage
// a move costs is a lower bound on what one move per commit would cost.
type RelicBenchmarkReport struct {
	Fixture                    RelicFixtureSpec          `json:"fixture"`
	FixtureMilliseconds        map[string]float64        `json:"fixtureMilliseconds"`
	Reorder                    []RelicReorderResult      `json:"reorder"`
	StoredLongestPositionBytes int                       `json:"storedLongestPositionBytes"`
	GitCommands                map[string]map[string]int `json:"gitCommands"`
	StorageBefore              GitStorageAccount         `json:"storageBefore"`
	StorageAfter               GitStorageAccount         `json:"storageAfter"`
	Cache                      DisposableCacheAccount    `json:"cache"`
	// TaskScenarios are every cli-* scenario; the two that synchronize,
	// cli-next and cli-update-autosync, publish to an origin whose refs are
	// loose, as a push leaves them. PackedOriginTaskScenarios are those two
	// again with origin's refs packed by git pack-refs --all, as a hosted or
	// maintained origin keeps them. In both, each side is a copy of a checkout
	// that git pack-refs --all and git gc were run on last.
	TaskScenarios             []RelicTaskComparison `json:"taskScenarios"`
	PackedOriginTaskScenarios []RelicTaskComparison `json:"packedOriginTaskScenarios"`
}

// relicSyncingTaskScenarios are the cli-* scenarios that synchronize with an
// origin, measured with that origin's refs loose and packed.
var relicSyncingTaskScenarios = []string{"cli-next", "cli-update-autosync"}

type relicDependencies struct {
	measureCommand func(context.Context, CommandSpec) CommandMeasurement
}

// relicVerifier checks a measured command's standard output.
type relicVerifier func(stdout []byte) error

// relicRun is one relic benchmark run in progress.
type relicRun struct {
	ctx          context.Context
	spec         RelicRunSpec
	dependencies relicDependencies
	// environment is added to every Workbook command: a configuration home
	// inside the run's root, so a setup measured here writes no operator's
	// user configuration.
	environment []string
	results     map[string]*ScenarioResult
	gitCommands map[string]map[string]int
}

func (r *relicRun) command(directory string, args []string) CommandSpec {
	return CommandSpec{
		Binary:      r.spec.WorkbookBinary,
		Args:        args,
		Directory:   directory,
		Environment: r.environment,
		Timeout:     r.spec.CommandTimeout,
	}
}

// measure times one sample of a scenario and checks its answer. A command
// that failed is kept as it is, and the report it lands in fails the run; a
// command that succeeded with the wrong answer fails the run here, naming the
// scenario, since its time is not the time of the work its name says.
func (r *relicRun) measure(name, directory string, args []string, verify relicVerifier) (CommandMeasurement, error) {
	measurement := r.dependencies.measureCommand(r.ctx, r.command(directory, args))
	return measurement, r.record(name, measurement, verify)
}

// record keeps one sample of a scenario, with its Git command tally, and checks
// the answer of a command that succeeded.
func (r *relicRun) record(name string, measurement CommandMeasurement, verify relicVerifier) error {
	result, found := r.results[name]
	if !found {
		result = &ScenarioResult{Name: name, Surface: "cold-cli"}
		r.results[name] = result
	}
	result.Samples = append(result.Samples, measurement.Sample)
	tally := r.gitCommands[name]
	if tally == nil {
		tally = map[string]int{}
		r.gitCommands[name] = tally
	}
	for command, count := range measurement.GitCommands {
		tally[command] = max(tally[command], count)
	}
	if !sampleSucceeded(measurement.Sample) || verify == nil {
		return nil
	}
	if err := verify(measurement.Stdout); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// measureMergeOrRefusal times one sync of a clone whose save of relicID's body
// has to be merged. A merge may be refused as too costly to make, and the sync
// then exits with the conflict code. That exit, with a report of that refusal
// and no other, is the work the scenario times, so its sample is recorded as
// completed, with exit code 0; any other failure is kept as it is.
func (r *relicRun) measureMergeOrRefusal(name, directory, relicID string) error {
	measurement := r.dependencies.measureCommand(r.ctx, r.command(directory, []string{"sync", "--json"}))
	conflict := core.ExitCode(core.Errorf(core.CategoryConflict, "merge refused"))
	if measurement.Sample.ExitCode == conflict && !measurement.Sample.TimedOut &&
		verifySyncMergeRefused(relicID)(measurement.Stdout) == nil {
		measurement.Sample.ExitCode, measurement.Sample.Error = 0, ""
		return r.record(name, measurement, nil)
	}
	return r.record(name, measurement, verifySyncMerged(1))
}

// untimed runs a command that is setup rather than measurement: a warm-up, or
// a read that tells the next sample what to do. It must succeed.
func (r *relicRun) untimed(what, directory string, args []string, verify relicVerifier) ([]byte, error) {
	measurement := r.dependencies.measureCommand(r.ctx, r.command(directory, args))
	if !sampleSucceeded(measurement.Sample) {
		return nil, fmt.Errorf("%s: workbook %s failed with exit code %d: %s",
			what, strings.Join(args, " "), measurement.Sample.ExitCode, measurement.Sample.Error)
	}
	if verify != nil {
		if err := verify(measurement.Stdout); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
	}
	return measurement.Stdout, nil
}

// warm runs the read once untimed, then measures it.
func (r *relicRun) warm(name, directory string, args []string, verify relicVerifier) error {
	if _, err := r.untimed(name+" warm-up", directory, args, verify); err != nil {
		return err
	}
	_, err := r.measure(name, directory, args, verify)
	return err
}

// resultData decodes a workbook.result envelope's data member into target.
func resultData(stdout []byte, target any) error {
	var envelope remoteResultEnvelope
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	if envelope.Format != workbookResultFormat || envelope.Version != workbookJSONVersion {
		return fmt.Errorf("result is %s version %d, want %s version %d",
			envelope.Format, envelope.Version, workbookResultFormat, workbookJSONVersion)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode result data: %w", err)
	}
	return nil
}

func verifyRelicListCount(want int) relicVerifier {
	return func(stdout []byte) error {
		var relics []json.RawMessage
		if err := resultData(stdout, &relics); err != nil {
			return err
		}
		if len(relics) != want {
			return fmt.Errorf("listed %d relics, want %d", len(relics), want)
		}
		return nil
	}
}

func verifyRebuildRelicCount(want int) relicVerifier {
	return func(stdout []byte) error {
		var rebuilt struct {
			RelicCount int `json:"relicCount"`
		}
		if err := resultData(stdout, &rebuilt); err != nil {
			return err
		}
		if rebuilt.RelicCount != want {
			return fmt.Errorf("rebuild indexed %d relics, want %d", rebuilt.RelicCount, want)
		}
		return nil
	}
}

func verifyBodyLength(want int) relicVerifier {
	return func(stdout []byte) error {
		if len(stdout) != want {
			return fmt.Errorf("body is %d bytes, want %d", len(stdout), want)
		}
		return nil
	}
}

// verifyRelicHistory checks a part's whole history: want changes, each one a
// body save, and so as many body versions.
func verifyRelicHistory(want int) relicVerifier {
	return func(stdout []byte) error {
		var history core.RelicHistory
		if err := resultData(stdout, &history); err != nil {
			return err
		}
		if len(history.Changes) != want || len(history.BodyVersions) != want {
			return fmt.Errorf("history lists %d changes and %d body versions, want %d of each",
				len(history.Changes), len(history.BodyVersions), want)
		}
		return nil
	}
}

// verifyRelicBodyDiff checks a part's diff: that one changed part, its body
// compared line by line, with hunks and within the line diff's bound, and with
// from one to maxLines deleted lines and from one to maxLines inserted lines.
func verifyRelicBodyDiff(maxLines int) relicVerifier {
	return func(stdout []byte) error {
		var diff core.RelicDiff
		if err := resultData(stdout, &diff); err != nil {
			return err
		}
		if len(diff.Parts) != 1 || diff.Parts[0].Kind != core.RelicPartChanged {
			return fmt.Errorf("diff lists %+v, want one changed part", diff.Parts)
		}
		body := diff.Parts[0].Body
		if body == nil || !body.Text || body.Approximate || len(body.Hunks) == 0 {
			return fmt.Errorf("body diff is %+v, want an exact text diff with hunks", body)
		}
		var deleted, inserted int
		for _, hunk := range body.Hunks {
			for _, line := range hunk.Lines {
				switch line.Kind {
				case core.DiffDelete:
					deleted++
				case core.DiffInsert:
					inserted++
				}
			}
		}
		if deleted < 1 || deleted > maxLines || inserted < 1 || inserted > maxLines {
			return fmt.Errorf("body diff deletes %d lines and inserts %d, want 1 to %d of each",
				deleted, inserted, maxLines)
		}
		return nil
	}
}

type relicChangeResult struct {
	Changed bool                     `json:"changed"`
	Parts   []core.RelicDocumentPart `json:"parts"`
}

func verifyRelicChanged(parts int) relicVerifier {
	return func(stdout []byte) error {
		var result relicChangeResult
		if err := resultData(stdout, &result); err != nil {
			return err
		}
		if !result.Changed {
			return fmt.Errorf("the relic did not change")
		}
		if parts >= 0 && len(result.Parts) != parts {
			return fmt.Errorf("the change reports %d parts, want %d", len(result.Parts), parts)
		}
		return nil
	}
}

func verifyRelicShowParts(parts int, bodyBytes int64) relicVerifier {
	return func(stdout []byte) error {
		var document core.RelicDocument
		if err := resultData(stdout, &document); err != nil {
			return err
		}
		if len(document.Parts) != parts {
			return fmt.Errorf("shows %d parts, want %d", len(document.Parts), parts)
		}
		if bodyBytes >= 0 {
			if body := document.Parts[0].Body; body == nil || body.Bytes != bodyBytes {
				return fmt.Errorf("part body is %+v, want %d bytes", body, bodyBytes)
			}
		}
		return nil
	}
}

// verifyRelicLiveBodies checks a relic create or show that reports parts
// bodies of totalBytes together. A create must also report a change.
func verifyRelicLiveBodies(parts int, totalBytes int64) relicVerifier {
	return func(stdout []byte) error {
		var result struct {
			Changed *bool                    `json:"changed"`
			Parts   []core.RelicDocumentPart `json:"parts"`
		}
		if err := resultData(stdout, &result); err != nil {
			return err
		}
		if result.Changed != nil && !*result.Changed {
			return fmt.Errorf("the relic did not change")
		}
		if len(result.Parts) != parts {
			return fmt.Errorf("reports %d parts, want %d", len(result.Parts), parts)
		}
		var total int64
		for _, part := range result.Parts {
			if part.Body != nil {
				total += part.Body.Bytes
			}
		}
		if total != totalBytes {
			return fmt.Errorf("live bodies are %d bytes, want %d", total, totalBytes)
		}
		return nil
	}
}

// relicPartBase reads one part's body version, untimed, as the edit after it
// names with --base.
func (r *relicRun) relicPartBase(what, directory, address string) (string, error) {
	shown, err := r.untimed(what, directory, []string{"relic", "show", address, "--json"}, verifyRelicShowParts(1, -1))
	if err != nil {
		return "", err
	}
	var current core.RelicDocument
	if err := resultData(shown, &current); err != nil {
		return "", err
	}
	if current.Parts[0].Body == nil {
		return "", fmt.Errorf("%s: part %s has no body", what, address)
	}
	return current.Parts[0].Body.Version, nil
}

// measureBodySave measures one sample of saving a new body of size bytes over
// a part, with an untimed read for its base first.
func (b *relicBench) measureBodySave(name, directory, relicID, partID string, size int, seed int64) error {
	base, err := b.run.relicPartBase(name+" base", directory, relicID+"#"+partID)
	if err != nil {
		return err
	}
	body := filepath.Join(b.root, fmt.Sprintf("%s-%d.md", name, seed))
	if err := os.WriteFile(body, seededWords(rand.New(rand.NewSource(seed)), size), 0o644); err != nil {
		return err
	}
	_, err = b.run.measure(name, directory, []string{
		"relic", "update", relicID, "--part", partID, "--body-file", body, "--base", base, "--no-sync", "--json",
	}, verifyRelicChanged(-1))
	if removeErr := os.Remove(body); err == nil {
		err = removeErr
	}
	return err
}

func verifyTaskRelicLinks(want int) relicVerifier {
	return func(stdout []byte) error {
		var task struct {
			Relics []core.TaskRelicLink `json:"relics"`
		}
		if err := resultData(stdout, &task); err != nil {
			return err
		}
		if len(task.Relics) != want {
			return fmt.Errorf("the task shows %d relic links, want %d", len(task.Relics), want)
		}
		return nil
	}
}

// verifyRelicIncomingLinks checks a part show: the one part, and want links to
// it from other relics.
func verifyRelicIncomingLinks(want int) relicVerifier {
	return func(stdout []byte) error {
		var document core.RelicDocument
		if err := resultData(stdout, &document); err != nil {
			return err
		}
		if len(document.Parts) != 1 || len(document.LinkedFrom) != want {
			return fmt.Errorf("shows %d parts and %d incoming links, want 1 and %d",
				len(document.Parts), len(document.LinkedFrom), want)
		}
		return nil
	}
}

type relicSyncPhase struct {
	Relics []gitstore.SyncRelicResult `json:"relics"`
}

// verifySyncRelics checks the relics a sync published or a fetch brought in.
func verifySyncRelics(push bool, want int) relicVerifier {
	return func(stdout []byte) error {
		var phase relicSyncPhase
		if push {
			var synced struct {
				Push relicSyncPhase `json:"push"`
			}
			if err := resultData(stdout, &synced); err != nil {
				return err
			}
			phase = synced.Push
		} else if err := resultData(stdout, &phase); err != nil {
			return err
		}
		moved := 0
		for _, relic := range phase.Relics {
			if (push && relic.Status == "published") || (!push && relic.Status == "fast-forwarded") {
				moved++
			}
		}
		if moved != want {
			return fmt.Errorf("moved %d relics (%+v), want %d", moved, phase.Relics, want)
		}
		return nil
	}
}

// mergeSyncReport is what the merge scenarios read of a sync's envelope.
type mergeSyncReport struct {
	relics    []gitstore.SyncRelicResult
	warnings  []core.Warning
	conflicts []core.RelicConflict
}

func readMergeSync(stdout []byte) (mergeSyncReport, error) {
	var synced struct {
		Fetch relicSyncPhase `json:"fetch"`
	}
	if err := resultData(stdout, &synced); err != nil {
		return mergeSyncReport{}, err
	}
	var envelope struct {
		Warnings  []core.Warning       `json:"warnings"`
		Conflicts []core.RelicConflict `json:"relicConflict"`
	}
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return mergeSyncReport{}, fmt.Errorf("decode result: %w", err)
	}
	return mergeSyncReport{synced.Fetch.Relics, envelope.Warnings, envelope.Conflicts}, nil
}

// verifySyncMerged checks a sync that merged the saves of want relics: each
// reported reconciled with one relic-body-merged warning, and no conflict.
func verifySyncMerged(want int) relicVerifier {
	return func(stdout []byte) error {
		report, err := readMergeSync(stdout)
		if err != nil {
			return err
		}
		reconciled := 0
		for _, relic := range report.relics {
			if relic.Status == gitstore.SyncReconciled {
				reconciled++
			}
		}
		merged := 0
		for _, warning := range report.warnings {
			if warning.Code == core.WarningRelicBodyMerged {
				merged++
			}
		}
		if reconciled != want || merged != want || len(report.conflicts) != 0 {
			return fmt.Errorf("reconciled %d relics with %d merge warnings and %d conflicts, want %d, %d and none (%+v)",
				reconciled, merged, len(report.conflicts), want, want, report.conflicts)
		}
		return nil
	}
}

// verifySyncMergeRefused checks a sync that refused to merge relicID's body as
// too many changes to compare: one such body conflict, and the relic
// conflicted.
func verifySyncMergeRefused(relicID string) relicVerifier {
	return func(stdout []byte) error {
		report, err := readMergeSync(stdout)
		if err != nil {
			return err
		}
		if len(report.conflicts) != 1 || report.conflicts[0].RelicID != relicID ||
			report.conflicts[0].Body == nil || report.conflicts[0].Body.Reason != core.RelicMergeTooManyChanges {
			return fmt.Errorf("conflicts %+v, want %s refused as %s", report.conflicts, relicID, core.RelicMergeTooManyChanges)
		}
		if !slices.ContainsFunc(report.relics, func(relic gitstore.SyncRelicResult) bool {
			return relic.RelicID == relicID && relic.Status == gitstore.SyncConflicted
		}) {
			return fmt.Errorf("relics %+v, want %s conflicted", report.relics, relicID)
		}
		return nil
	}
}

func verifyValidated(stdout []byte) error {
	var validated struct {
		Invalid int `json:"invalid"`
	}
	if err := resultData(stdout, &validated); err != nil {
		return err
	}
	if validated.Invalid != 0 {
		return fmt.Errorf("validation found %d invalid tasks", validated.Invalid)
	}
	return nil
}

// RunRelicBenchmark builds the relic set beside the task fixture, publishes
// it to a bare origin, clones it twice, and measures every relic scenario,
// then the cli-* scenarios without and with relics. Every fixture stage is
// bounded by the run's fixture timeout and recorded, untimed by any scenario,
// in the report's fixtureMilliseconds.
func RunRelicBenchmark(ctx context.Context, spec RelicRunSpec, root string) ([]ScenarioResult, *RelicBenchmarkReport, error) {
	return runRelicBenchmark(ctx, spec, root, relicDependencies{measureCommand: MeasureCommandOutput})
}

func runRelicBenchmark(ctx context.Context, spec RelicRunSpec, root string, dependencies relicDependencies) ([]ScenarioResult, *RelicBenchmarkReport, error) {
	if spec.Samples < 1 {
		return nil, nil, fmt.Errorf("samples must be positive")
	}
	if spec.CommandTimeout <= 0 {
		return nil, nil, fmt.Errorf("command timeout must be positive")
	}
	if err := requireFixtureTimeout(&spec.RunSpec); err != nil {
		return nil, nil, err
	}
	if root == "" {
		return nil, nil, fmt.Errorf("relic benchmark root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve relic benchmark root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create relic benchmark root: %w", err)
	}
	run := &relicRun{
		ctx:          ctx,
		spec:         spec,
		dependencies: dependencies,
		environment:  []string{"XDG_CONFIG_HOME=" + filepath.Join(absRoot, "config")},
		results:      map[string]*ScenarioResult{},
		gitCommands:  map[string]map[string]int{},
	}
	bench := &relicBench{run: run, root: absRoot, report: &RelicBenchmarkReport{
		Fixture:             spec.Relics,
		FixtureMilliseconds: map[string]float64{},
		GitCommands:         run.gitCommands,
	}}
	for _, step := range []func() error{
		bench.buildFixture,
		bench.publishAndClone,
		bench.measureCreates,
		bench.measureMain,
		bench.measureRemote,
		bench.measureMerges,
		bench.measureFreshClones,
		bench.measureValidation,
		bench.accountStorage,
		bench.compareTaskScenarios,
	} {
		if err := step(); err != nil {
			return nil, nil, err
		}
	}
	results := make([]ScenarioResult, 0, len(relicScenarioNames))
	for _, name := range relicScenarioNames {
		result, found := run.results[name]
		if !found {
			return nil, nil, fmt.Errorf("relic scenario %s was not measured", name)
		}
		result.Summary = Summarize(result.Samples)
		results = append(results, *result)
	}
	return results, bench.report, nil
}

// relicBench holds what the stages of one relic run share.
type relicBench struct {
	run      *relicRun
	root     string
	report   *RelicBenchmarkReport
	builder  *relicFixtureBuilder
	fixture  RelicFixture
	main     string
	template string
	// plainTemplate and taskTemplate are what the task comparison's two sides
	// copy: the task fixture alone, and a copy of template. Both are packed
	// with git pack-refs --all and git gc last.
	plainTemplate string
	plainFixture  Fixture
	taskTemplate  string
	// creates is a copy of the fixture the scenarios that create relics run
	// in, so the relics they add are in no other scenario's repository and in
	// neither storage figure.
	creates string
	origin  string
	cloneA  string
	cloneB  string
	// changes numbers the body edits made in clone B, so each is new text.
	changes int
}

// stage runs one untimed fixture step under the fixture timeout and records
// how long it took.
func (b *relicBench) stage(name string, step func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(b.run.ctx, b.run.spec.FixtureTimeout)
	defer cancel()
	started := time.Now()
	if err := step(ctx); err != nil {
		return fmt.Errorf("relic fixture %s: %w", name, err)
	}
	b.report.FixtureMilliseconds[name] = durationAsMilliseconds(time.Since(started))
	return nil
}

func (b *relicBench) relicCount() int {
	return b.run.spec.Relics.Relics + relicFixtureExtraRelics
}

func (b *relicBench) buildFixture() error {
	spec := b.run.spec
	b.main = filepath.Join(b.root, "main")
	var tasks Fixture
	if err := b.stage("tasks", func(ctx context.Context) (err error) {
		tasks, err = BuildFixture(ctx, b.main, spec.Fixture)
		return err
	}); err != nil {
		return err
	}
	if err := b.stage("relics", func(ctx context.Context) (err error) {
		if b.builder, err = newRelicFixtureBuilder(ctx, tasks, spec.Relics); err != nil {
			return err
		}
		return b.builder.createRelics(ctx)
	}); err != nil {
		return err
	}
	if err := b.stage("storage-before", func(ctx context.Context) (err error) {
		if err := prepareStorageRepository(ctx, spec.FixtureTimeout, b.main); err != nil {
			return err
		}
		b.report.StorageBefore, err = measureGitStorage(ctx, spec.FixtureTimeout, b.main)
		return err
	}); err != nil {
		return err
	}
	if err := b.stage("links-and-moves", func(ctx context.Context) (err error) {
		if err := b.builder.linkAndMove(ctx); err != nil {
			return err
		}
		b.fixture, err = b.builder.finish(ctx)
		return err
	}); err != nil {
		return err
	}
	if err := b.stage("stored-positions", func(ctx context.Context) error {
		document, err := b.builder.service.Show(ctx, b.fixture.LargeRelic, core.RelicShowOptions{})
		if err != nil {
			return err
		}
		for _, part := range document.Parts {
			b.report.StoredLongestPositionBytes = max(b.report.StoredLongestPositionBytes, len(part.Position))
		}
		return nil
	}); err != nil {
		return err
	}
	if err := b.stage("reorder", func(context.Context) error {
		for _, pattern := range relicReorderPatterns {
			result, err := MeasureRelicReorder(pattern, spec.Relics.LargeParts, spec.Relics.Moves, spec.Relics.Seed)
			if err != nil {
				return err
			}
			b.report.Reorder = append(b.report.Reorder, result)
		}
		return nil
	}); err != nil {
		return err
	}
	// The task comparison copies the fixture as it stands now, before it has
	// an origin, so its scenarios publish to an origin of their own as they do
	// on the task fixture alone.
	b.template = filepath.Join(b.root, "template")
	return b.stage("template", func(context.Context) error {
		return os.CopyFS(b.template, os.DirFS(b.main))
	})
}

func (b *relicBench) publishAndClone() error {
	spec := b.run.spec
	b.origin = filepath.Join(b.root, "origin.git")
	if err := b.stage("publish", func(ctx context.Context) error {
		return publishRelicFixture(ctx, spec, b.main, b.origin)
	}); err != nil {
		return err
	}
	b.cloneA = filepath.Join(b.root, "clone-a")
	b.cloneB = filepath.Join(b.root, "clone-b")
	return b.stage("clones", func(ctx context.Context) error {
		for _, clone := range []string{b.cloneA, b.cloneB} {
			if err := b.setUpClone(ctx, b.origin, clone, b.relicCount()); err != nil {
				return err
			}
		}
		return nil
	})
}

// publishRelicFixture makes a bare origin and pushes source's Workbook refs to
// it.
func publishRelicFixture(ctx context.Context, spec RelicRunSpec, source, origin string) error {
	if err := initBareFixtureOrigin(ctx, spec.FixtureTimeout, spec.Fixture.ObjectFormat, origin); err != nil {
		return err
	}
	if _, _, err := runRepositoryGit(ctx, spec.FixtureTimeout, source, "remote", "add", "origin", origin); err != nil {
		return err
	}
	_, _, err := runRepositoryGit(ctx, spec.FixtureTimeout, source, "push", "--quiet", "origin", "refs/workbook/*:refs/workbook/*")
	return err
}

// setUpClone clones origin to destination, sets it up and indexes its relics,
// of which it must then list relics.
func (b *relicBench) setUpClone(ctx context.Context, origin, destination string, relics int) error {
	if err := cloneRelicOrigin(ctx, origin, destination); err != nil {
		return err
	}
	name := filepath.Base(destination)
	if _, err := b.run.untimed("set up "+name, destination, []string{"setup", "--json"}, nil); err != nil {
		return err
	}
	_, err := b.run.untimed("index "+name, destination, []string{"relic", "list", "--json"}, verifyRelicListCount(relics))
	return err
}

// cloneRelicOrigin makes a plain clone of origin with the fixture's isolation
// settings. A clone fetches no Workbook refs; setup does.
func cloneRelicOrigin(ctx context.Context, origin, destination string) error {
	if err := runFixtureGitInRoot(ctx, destination, "clone", "--quiet", origin, destination); err != nil {
		return err
	}
	return configureFixtureRepository(ctx, destination)
}

func (b *relicBench) countRelicRefs(directory string) (int, error) {
	ctx, cancel := context.WithTimeout(b.run.ctx, b.run.spec.FixtureTimeout)
	defer cancel()
	refs, _, err := runRepositoryGit(ctx, b.run.spec.FixtureTimeout, directory, "for-each-ref", "--format=%(refname)", relicRefPrefix)
	if err != nil {
		return 0, err
	}
	return countRefLines(refs), nil
}

// measureCreates runs the scenarios that create relics in a copy of the
// fixture as it stood before it had an origin, the same state the main
// repository holds, and removes the copy when they are done.
func (b *relicBench) measureCreates() error {
	spec := b.run.spec
	relics, run := spec.Relics, b.run
	b.creates = filepath.Join(b.root, "creates")
	if err := b.stage("creates-copy", func(context.Context) error {
		return os.CopyFS(b.creates, os.DirFS(b.template))
	}); err != nil {
		return err
	}
	document, err := LargeRelicDocument(relics)
	if err != nil {
		return err
	}
	documentPath := filepath.Join(b.root, "large.json")
	if err := os.WriteFile(documentPath, document, 0o644); err != nil {
		return err
	}
	for range spec.Samples {
		if _, err := run.measure("relic-create-large", b.creates,
			[]string{"relic", "create", "--from-file", documentPath, "--no-sync", "--json"},
			verifyRelicChanged(relics.LargeParts)); err != nil {
			return err
		}
	}

	// A relic at the live-body limit: create one per sample, then show and
	// edit the first. The edit replaces the small last part with a body of
	// the same size, so the total stays at the limit.
	full, err := FullBodiesRelicDocument(relics)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(b.root, "bodies-16mib.json")
	if err := os.WriteFile(fullPath, full, 0o644); err != nil {
		return err
	}
	fullParts, fullBytes := FullBodiesRelicParts(relics), int64(relics.FullBodiesBytes)
	var fullRelic, fullSmallPart string
	for range spec.Samples {
		created, err := run.measure("relic-create-16mib", b.creates,
			[]string{"relic", "create", "--from-file", fullPath, "--no-sync", "--json"},
			verifyRelicLiveBodies(fullParts, fullBytes))
		if err != nil {
			return err
		}
		if fullRelic != "" || !sampleSucceeded(created.Sample) {
			continue
		}
		var result core.RelicMutationResult
		if err := resultData(created.Stdout, &result); err != nil {
			return err
		}
		fullRelic, fullSmallPart = result.Relic.ID, result.Parts[len(result.Parts)-1].ID
	}
	if err := os.Remove(fullPath); err != nil {
		return err
	}
	if fullRelic == "" {
		// Every create failed; the report fails the run on those samples.
		return os.RemoveAll(b.creates)
	}
	for range spec.Samples {
		if err := run.warm("relic-show-16mib", b.creates, []string{"relic", "show", fullRelic, "--json"},
			verifyRelicLiveBodies(fullParts, fullBytes)); err != nil {
			return err
		}
	}
	for sample := range spec.Samples {
		if err := b.measureBodySave("relic-edit-part-16mib", b.creates, fullRelic, fullSmallPart,
			relics.SmallBodyBytes, relics.Seed+int64(sample)+3000); err != nil {
			return err
		}
	}

	// A new version of the body relic's 1 MiB body. It runs here, not in the
	// main repository, so the megabyte each sample saves is in no storage
	// figure; the copy's body relic is the main repository's.
	for sample := range spec.Samples {
		if err := b.measureBodySave("relic-edit-body-1mib", b.creates, b.fixture.BodyRelic, b.fixture.LargeBodyPart,
			relics.LargeBodyBytes, relics.Seed+int64(sample)+4000); err != nil {
			return err
		}
	}
	return os.RemoveAll(b.creates)
}

// measureMain runs the scenarios on the main repository, in the plan's order.
func (b *relicBench) measureMain() error {
	spec := b.run.spec
	relics, fixture, run := spec.Relics, b.fixture, b.run
	samples := spec.Samples

	part := fixture.LargeRelic + "#" + fixture.LargeMovedPart
	for sample := range samples {
		shown, err := run.untimed("relic-edit-part-large base", b.main, []string{"relic", "show", part, "--json"}, verifyRelicShowParts(1, -1))
		if err != nil {
			return err
		}
		var current core.RelicDocument
		if err := resultData(shown, &current); err != nil {
			return err
		}
		body := filepath.Join(b.root, fmt.Sprintf("edit-%03d.md", sample+1))
		text := seededWords(rand.New(rand.NewSource(relics.Seed+int64(sample)+1000)), relics.LargePartBodyBytes)
		if err := os.WriteFile(body, text, 0o644); err != nil {
			return err
		}
		if _, err := run.measure("relic-edit-part-large", b.main, []string{
			"relic", "update", fixture.LargeRelic, "--part", fixture.LargeMovedPart,
			"--body-file", body, "--base", current.Parts[0].Body.Version, "--no-sync", "--json",
		}, verifyRelicChanged(-1)); err != nil {
			return err
		}
	}

	for range samples {
		if err := run.warm("relic-show-large", b.main, []string{"relic", "show", fixture.LargeRelic, "--json"},
			verifyRelicShowParts(relics.LargeParts, -1)); err != nil {
			return err
		}
	}

	for range samples {
		shown, err := run.untimed("relic-move-part-large order", b.main, []string{"relic", "show", fixture.LargeRelic, "--json"},
			verifyRelicShowParts(relics.LargeParts, -1))
		if err != nil {
			return err
		}
		var current core.RelicDocument
		if err := resultData(shown, &current); err != nil {
			return err
		}
		anchor := current.Parts[len(current.Parts)-1].ID
		if anchor == fixture.LargeMovedPart {
			anchor = current.Parts[0].ID
		}
		if _, err := run.measure("relic-move-part-large", b.main, []string{
			"relic", "update", fixture.LargeRelic, "--move-part", fixture.LargeMovedPart, "--after", anchor, "--no-sync", "--json",
		}, verifyRelicChanged(-1)); err != nil {
			return err
		}
	}

	small := fixture.BodyRelic + "#" + fixture.SmallBodyPart
	large := fixture.BodyRelic + "#" + fixture.LargeBodyPart
	history := fixture.HistoryRelic + "#" + fixture.HistoryPart
	reads := []relicRead{
		{"relic-show-part-2kib", []string{"relic", "show", small, "--json"}, verifyRelicShowParts(1, int64(relics.SmallBodyBytes))},
		{"relic-body-2kib", []string{"relic", "show", small, "--body"}, verifyBodyLength(relics.SmallBodyBytes)},
		{"relic-body-1mib", []string{"relic", "show", large, "--body"}, verifyBodyLength(relics.LargeBodyBytes)},
		{"relic-history-100", []string{"relic", "show", history, "--history", "--all", "--json"},
			verifyRelicHistory(relics.HistoryEdits + 1)},
		{"relic-diff-latest-100kib", []string{"relic", "diff", history, "--json"},
			verifyRelicBodyDiff(relicHistoryLinesPerEdit)},
		{"relic-diff-first-last-100kib",
			[]string{"relic", "diff", history, fixture.HistoryFirstVersion, fixture.HistoryLastVersion, "--json"},
			verifyRelicBodyDiff(relics.HistoryEdits * relicHistoryLinesPerEdit)},
	}
	for _, read := range reads {
		for range samples {
			if err := run.warm(read.name, b.main, read.args, read.verify); err != nil {
				return err
			}
		}
	}

	// The main repository holds the fixture's relics and no created one: a
	// relic created here would also be in the storage figures.
	listed := b.relicCount()
	if refs, err := b.countRelicRefs(b.main); err != nil {
		return err
	} else if refs != listed {
		return fmt.Errorf("the main repository holds %d relic refs, want the fixture's %d", refs, listed)
	}
	agendas := 0
	for index := range relics.Relics {
		if relicFixtureTypes[index%len(relicFixtureTypes)] == "agenda" {
			agendas++
		}
	}
	lists := []relicRead{
		{"relic-list", []string{"relic", "list", "--json"}, verifyRelicListCount(listed)},
		{"relic-list-type", []string{"relic", "list", "--type", "agenda", "--json"}, verifyRelicListCount(agendas)},
	}
	for _, read := range append(lists, relicReverseLookups(fixture)...) {
		for range samples {
			if err := run.warm(read.name, b.main, read.args, read.verify); err != nil {
				return err
			}
		}
	}
	return nil
}

// relicRead is a read scenario: the command it times and how its answer is
// checked.
type relicRead struct {
	name   string
	args   []string
	verify relicVerifier
}

// relicReverseLookups are the scenarios that ask what links to something: the
// hot and a typical task, and the hot relic part. All are judged against the
// reverse-lookup budget.
func relicReverseLookups(fixture RelicFixture) []relicRead {
	return []relicRead{
		{"relic-linked-to-hot", []string{"relic", "list", "--linked-to", fixture.HotTask, "--json"}, verifyRelicListCount(fixture.HotTaskLinks)},
		{"relic-linked-to-typical", []string{"relic", "list", "--linked-to", fixture.TypicalTask, "--json"}, verifyRelicListCount(fixture.TypicalTaskLinks)},
		{"task-show-linked-hot", []string{"show", fixture.HotTask, "--json"}, verifyTaskRelicLinks(fixture.HotTaskLinks)},
		{"relic-linked-to-part-hot", []string{"relic", "list", "--linked-to", fixture.HotRelicPart, "--json"}, verifyRelicListCount(fixture.HotRelicPartLinks)},
		{"relic-show-linked-part-hot", []string{"relic", "show", fixture.HotRelicPart, "--json"}, verifyRelicIncomingLinks(fixture.HotRelicPartLinks)},
	}
}

// editInCloneB gives count fixture relics, starting at first, one body edit
// each in clone B, through the relic service in process, as a teammate's
// edits would arrive. It is setup: the push that publishes them is measured.
func (b *relicBench) editInCloneB(first, count int) error {
	ctx, cancel := context.WithTimeout(b.run.ctx, b.run.spec.FixtureTimeout)
	defer cancel()
	_, service, err := b.relicServiceIn(ctx, b.cloneB)
	if err != nil {
		return err
	}
	relics := b.run.spec.Relics
	for offset := range count {
		index := (first + offset) % len(b.fixture.RelicIDs)
		relicID, partID := b.fixture.RelicIDs[index], b.builder.fixtureParts[index][0]
		shown, err := service.Show(ctx, relicID+"#"+partID, core.RelicShowOptions{})
		if err != nil {
			return err
		}
		b.changes++
		text := seededWords(rand.New(rand.NewSource(relics.Seed+int64(b.changes)+2000)), relics.BodyBytes)
		if _, err := service.Update(ctx, relicID, []core.RelicEdit{{
			Kind: core.RelicEditPart, Part: partID, Base: shown.Parts[0].Body.Version,
			Body: &core.RelicBodyInput{Format: relicFixtureBodyFormat, Content: text},
		}}); err != nil {
			return fmt.Errorf("edit relic %s in clone B: %w", relicID, err)
		}
	}
	return nil
}

// measureRemote pushes changed relics from clone B and fetches and reads them
// in clone A.
func (b *relicBench) measureRemote() error {
	run, relics := b.run, b.run.spec.Relics
	changed := relics.ChangedRelics
	for sample := range b.run.spec.Samples {
		if err := b.editInCloneB(sample*changed, changed); err != nil {
			return err
		}
		if _, err := run.measure("relic-push-100", b.cloneB, []string{"sync", "--json"}, verifySyncRelics(true, changed)); err != nil {
			return err
		}
		if _, err := run.measure("relic-fetch-100", b.cloneA, []string{"fetch", "--json"}, verifySyncRelics(false, changed)); err != nil {
			return err
		}
		list := []string{"relic", "list", "--json"}
		if _, err := run.measure("relic-refresh-100", b.cloneA, list, verifyRelicListCount(b.relicCount())); err != nil {
			return err
		}
		if _, err := run.measure("relic-refresh-0", b.cloneA, list, verifyRelicListCount(b.relicCount())); err != nil {
			return err
		}
		if err := b.editInCloneB(sample, 1); err != nil {
			return err
		}
		if _, err := run.untimed("relic-refresh-1 push", b.cloneB, []string{"sync", "--json"}, verifySyncRelics(true, 1)); err != nil {
			return err
		}
		if _, err := run.untimed("relic-refresh-1 fetch", b.cloneA, []string{"fetch", "--json"}, verifySyncRelics(false, 1)); err != nil {
			return err
		}
		if _, err := run.measure("relic-refresh-1", b.cloneA, list, verifyRelicListCount(b.relicCount())); err != nil {
			return err
		}
	}
	return nil
}

// mergeFixture is what the merge scenarios sync: two clones of an origin of
// their own, holding the relic fixture beside relics the clones save apart.
// first saves and publishes, and second, whose syncs are timed, saves from the
// versions it held before.
type mergeFixture struct {
	first, second string
	relics        []mergeRelic
	repetitive    mergeRelic
}

// measureMerges measures the two scenarios that sync body saves to be merged.
// Their relics are in a copy of the fixture with an origin of its own, so they
// are in no other scenario's repository and in neither storage figure.
func (b *relicBench) measureMerges() error {
	merges, err := b.buildMergeFixture()
	if err != nil {
		return err
	}
	if err := b.measureMergeSamples(merges); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(b.root, "merges"))
}

func (b *relicBench) buildMergeFixture() (mergeFixture, error) {
	spec := b.run.spec
	root := filepath.Join(b.root, "merges")
	source, origin := filepath.Join(root, "main"), filepath.Join(root, "origin.git")
	merges := mergeFixture{first: filepath.Join(root, "first"), second: filepath.Join(root, "second")}
	if err := b.stage("merge-relics", func(ctx context.Context) error {
		if err := os.CopyFS(source, os.DirFS(b.template)); err != nil {
			return err
		}
		repository, service, err := b.relicServiceIn(ctx, source)
		if err != nil {
			return err
		}
		if merges.relics, merges.repetitive, err = createMergeRelics(ctx, service, spec.Relics); err != nil {
			return err
		}
		repository.PackMovedRelicRefs(ctx)
		return publishRelicFixture(ctx, spec, source, origin)
	}); err != nil {
		return mergeFixture{}, err
	}
	listed := b.relicCount() + len(merges.relics) + 1
	err := b.stage("merge-clones", func(ctx context.Context) error {
		for _, clone := range []string{merges.first, merges.second} {
			if err := b.setUpClone(ctx, origin, clone, listed); err != nil {
				return err
			}
		}
		return nil
	})
	return merges, err
}

// measureMergeSamples measures each merge scenario once per sample, the many
// relics and then the repetitive one.
func (b *relicBench) measureMergeSamples(merges mergeFixture) error {
	relics := b.run.spec.Relics
	firstLine, secondLine := mergeEditLines(relics.MergeBodyLines)
	lines := relics.RepetitiveBodyBytes / 2
	for sample := 1; sample <= b.run.spec.Samples; sample++ {
		mark := func(side string, line int) func([]byte) []byte {
			return func(text []byte) []byte { return markLine(text, line, mergeMarker(side, sample)) }
		}
		if err := b.diverge("relic-sync-merge-100", merges, merges.relics,
			mark("first", firstLine), mark("second", secondLine)); err != nil {
			return err
		}
		if _, err := b.run.measure("relic-sync-merge-100", merges.second, []string{"sync", "--json"},
			verifySyncMerged(len(merges.relics))); err != nil {
			return err
		}

		// Each side saves two lines far apart, none touching the other
		// side's. Each sample writes other letters than the one before it, so
		// every save changes its lines.
		change := func(letters string, changed ...int) func([]byte) []byte {
			return func(text []byte) []byte {
				return changeRepetitiveLines(text, letters[sample%len(letters)], changed...)
			}
		}
		if err := b.diverge("relic-merge-repetitive-1mib", merges, []mergeRelic{merges.repetitive},
			change("abcdefg", 9, lines-10), change("hijklmn", lines/4, lines*3/4)); err != nil {
			return err
		}
		if err := b.run.measureMergeOrRefusal("relic-merge-repetitive-1mib", merges.second, merges.repetitive.id); err != nil {
			return err
		}
	}
	return nil
}

// diverge leaves the two clones holding different saves of each of relics, both
// from the version origin holds: the first clone catches up, saves and
// publishes, and the second saves without synchronizing.
func (b *relicBench) diverge(name string, merges mergeFixture, relics []mergeRelic, first, second func([]byte) []byte) error {
	if _, err := b.run.untimed(name+" catch up", merges.first, []string{"fetch", "--json"}, nil); err != nil {
		return err
	}
	if err := b.saveMergeRelics(merges.first, relics, first); err != nil {
		return err
	}
	if _, err := b.run.untimed(name+" publish", merges.first, []string{"sync", "--json"},
		verifySyncRelics(true, len(relics))); err != nil {
		return err
	}
	return b.saveMergeRelics(merges.second, relics, second)
}

// saveMergeRelics saves a changed body of each of relics in the clone at
// directory, through the relic service in process, which synchronizes nothing,
// as relic update --no-sync does not.
func (b *relicBench) saveMergeRelics(directory string, relics []mergeRelic, change func([]byte) []byte) error {
	ctx, cancel := context.WithTimeout(b.run.ctx, b.run.spec.FixtureTimeout)
	defer cancel()
	repository, service, err := b.relicServiceIn(ctx, directory)
	if err != nil {
		return err
	}
	for _, relic := range relics {
		shown, err := service.Show(ctx, relic.address(), core.RelicShowOptions{Bodies: true})
		if err != nil {
			return err
		}
		if len(shown.Parts) != 1 || shown.Parts[0].Body == nil || shown.Parts[0].Body.Text == nil {
			return fmt.Errorf("merge relic %s has no text body", relic.id)
		}
		body := shown.Parts[0].Body
		if _, err := service.Update(ctx, relic.id, []core.RelicEdit{{
			Kind: core.RelicEditPart, Part: relic.part, Base: body.Version,
			Body: &core.RelicBodyInput{Format: relicFixtureBodyFormat, Content: change([]byte(*body.Text))},
		}}); err != nil {
			return fmt.Errorf("save relic %s in %s: %w", relic.id, filepath.Base(directory), err)
		}
	}
	repository.PackMovedRelicRefs(ctx)
	return nil
}

// relicServiceIn is a relic service over the repository at directory, as a
// teammate's command would run there.
func (b *relicBench) relicServiceIn(ctx context.Context, directory string) (*gitstore.Repository, core.RelicService, error) {
	repository, err := gitstore.Open(ctx, directory)
	if err != nil {
		return nil, core.RelicService{}, err
	}
	return repository, core.RelicService{
		Config: b.fixture.Config,
		Reader: repository,
		Writer: repository,
		Tasks:  repository,
		IDs:    core.CryptoULIDSource{Now: time.Now},
		Actor:  benchmarkActorID,
	}, nil
}

// measureFreshClones sets up, rebuilds and first reads a new clone per sample.
func (b *relicBench) measureFreshClones() error {
	run := b.run
	for sample := range run.spec.Samples {
		clone := filepath.Join(b.root, fmt.Sprintf("fresh-%03d", sample+1))
		if err := b.stage(fmt.Sprintf("fresh-clone-%03d", sample+1), func(ctx context.Context) error {
			return cloneRelicOrigin(ctx, b.origin, clone)
		}); err != nil {
			return err
		}
		if _, err := run.measure("relic-clone-setup", clone, []string{"setup", "--json"}, func([]byte) error {
			refs, err := b.countRelicRefs(clone)
			if err != nil {
				return err
			}
			if refs != b.relicCount() {
				return fmt.Errorf("the clone holds %d relic refs, want %d", refs, b.relicCount())
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := run.measure("relic-rebuild-fresh-clone", clone, []string{"rebuild", "--json"}, verifyRebuildRelicCount(b.relicCount())); err != nil {
			return err
		}
		cache := filepath.Join(clone, ".git", "workbook", projectionCacheFilename)
		for _, path := range append([]string{cache}, sidecarPaths(cache)...) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if _, err := run.measure("relic-first-read-fresh-clone", clone, []string{"relic", "list", "--json"}, verifyRelicListCount(b.relicCount())); err != nil {
			return err
		}
		if err := os.RemoveAll(clone); err != nil {
			return err
		}
	}
	return nil
}

func sidecarPaths(path string) []string {
	paths := make([]string, len(sqliteSidecarSuffixes))
	for index, suffix := range sqliteSidecarSuffixes {
		paths[index] = path + suffix
	}
	return paths
}

// measureValidation runs full validation once: it is uncached for relics, so
// one sample is the whole story.
func (b *relicBench) measureValidation() error {
	_, err := b.run.measure("relic-validate-full", b.main, []string{"validate", "--full", "--json"}, verifyValidated)
	return err
}

func (b *relicBench) accountStorage() error {
	spec := b.run.spec
	return b.stage("storage-after", func(ctx context.Context) (err error) {
		if b.report.Cache, err = measureDisposableCaches(ctx, spec.FixtureTimeout, b.main); err != nil {
			return err
		}
		if err := prepareStorageRepository(ctx, spec.FixtureTimeout, b.main); err != nil {
			return err
		}
		b.report.StorageAfter, err = measureGitStorage(ctx, spec.FixtureTimeout, b.main)
		return err
	})
}

// compareTaskScenarios runs every cli-* scenario on the task fixture alone and
// on a copy of the relic fixture, with the same spec and samples, and then the
// ones that synchronize again with their origin's refs packed.
func (b *relicBench) compareTaskScenarios() error {
	var names []string
	for _, name := range scenarioRegistry {
		if strings.HasPrefix(name, "cli-") {
			names = append(names, name)
		}
	}
	// The two sides have to differ by the relic set and nothing else. They
	// did not: the relic fixture's repository is packed with git pack-refs
	// --all and git gc for the storage figures and written to again after it,
	// while a task fixture comes straight out of git fast-import. Git reads
	// every ref a checkout holds, and the commit at its tip, when it fetches
	// and when it pushes, and a loose ref or a loose commit costs more to read
	// than a packed one, so how each side happened to be stored showed up as a
	// difference between them. Both sides are therefore copies of a template
	// that the same two commands were run on last.
	spec := b.run.spec
	b.plainTemplate = filepath.Join(b.root, "task-template-without")
	if err := b.stage("task-template-without", func(ctx context.Context) (err error) {
		if b.plainFixture, err = BuildFixture(ctx, b.plainTemplate, spec.Fixture); err != nil {
			return err
		}
		return prepareStorageRepository(ctx, spec.FixtureTimeout, b.plainTemplate)
	}); err != nil {
		return err
	}
	b.taskTemplate = filepath.Join(b.root, "task-template-with")
	if err := b.stage("task-template-with", func(ctx context.Context) error {
		if err := os.CopyFS(b.taskTemplate, os.DirFS(b.template)); err != nil {
			return err
		}
		return prepareStorageRepository(ctx, spec.FixtureTimeout, b.taskTemplate)
	}); err != nil {
		return err
	}
	comparisons, err := b.compareTaskScenariosOnce("task-scenarios", names, false)
	if err != nil {
		return err
	}
	b.report.TaskScenarios = comparisons
	b.report.PackedOriginTaskScenarios, err = b.compareTaskScenariosOnce("task-scenarios-packed-origin", relicSyncingTaskScenarios, true)
	return err
}

// compareTaskScenariosOnce runs names without and with relics, recording the
// two fixture stages under stage plus -without and -with.
func (b *relicBench) compareTaskScenariosOnce(stage string, names []string, packOrigin bool) ([]RelicTaskComparison, error) {
	base := scenarioDependencies{
		buildFixture:      copiedFixture(b.plainTemplate, b.plainFixture),
		prepareProjection: prepareProjection,
		measureCommand:    b.run.dependencies.measureCommand,
		cleanupFixture:    os.RemoveAll,
		packOrigin:        packOrigin,
	}
	var without, with []ScenarioResult
	if err := b.stage(stage+"-without", func(ctx context.Context) (err error) {
		without, err = runColdCLI(b.run.ctx, b.run.spec.RunSpec, filepath.Join(b.root, stage+"-without"), names, base)
		return err
	}); err != nil {
		return nil, err
	}
	withRelics := base
	withRelics.buildFixture = copiedFixture(b.taskTemplate, b.fixture.Fixture)
	if err := b.stage(stage+"-with", func(ctx context.Context) (err error) {
		with, err = runColdCLI(b.run.ctx, b.run.spec.RunSpec, filepath.Join(b.root, stage+"-with"), names, withRelics)
		return err
	}); err != nil {
		return nil, err
	}
	comparisons := make([]RelicTaskComparison, 0, len(names))
	for index, name := range names {
		for _, result := range []ScenarioResult{without[index], with[index]} {
			for _, sample := range result.Samples {
				if !sampleSucceeded(sample) {
					return nil, fmt.Errorf("%s comparison %s failed: exit code %d: %s", stage, name, sample.ExitCode, sample.Error)
				}
			}
		}
		comparisons = append(comparisons, RelicTaskComparison{
			Name: name, Without: without[index].Summary, With: with[index].Summary,
		})
	}
	return comparisons, nil
}

// copiedFixture returns a fixture builder that copies template to the root it
// is given and describes the copy as fixture, with that root.
func copiedFixture(template string, fixture Fixture) func(context.Context, string, FixtureSpec) (Fixture, error) {
	return func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return Fixture{}, err
		}
		if err := os.MkdirAll(filepath.Dir(absRoot), 0o755); err != nil {
			return Fixture{}, err
		}
		if err := os.CopyFS(absRoot, os.DirFS(template)); err != nil {
			return Fixture{}, fmt.Errorf("copy the fixture template: %w", err)
		}
		fixture.Root = absRoot
		return fixture, nil
	}
}

// writeRelicMarkdown writes the ## Relics section: the fixture, the reorder
// results, the Git commands of the reverse lookups, the refreshes, the fetch
// and the merges, storage before and after by class, and the task comparison.
func writeRelicMarkdown(w io.Writer, report *RelicBenchmarkReport) error {
	if report == nil {
		return nil
	}
	var out strings.Builder
	fixture := report.Fixture
	first, second := mergeEditLines(fixture.MergeBodyLines)
	fmt.Fprintln(&out, "\n## Relics")
	fmt.Fprintf(&out, "\nFixture: %d relics × %d parts of %d bytes, %d links to tasks and %d to parts of other relics each; a large relic of %d parts × %d bytes; body parts of %d and %d bytes; %d moves in updates of %d; %d changed relics per sync; seed %d.\n",
		fixture.Relics, fixture.PartsPerRelic, fixture.BodyBytes, fixture.LinksPerRelic, fixture.RelicLinksPerRelic,
		fixture.LargeParts, fixture.LargePartBodyBytes, fixture.LargeBodyBytes, fixture.SmallBodyBytes,
		fixture.Moves, fixture.MovesPerUpdate, fixture.ChangedRelics, fixture.Seed)
	fmt.Fprintf(&out, "relic-create-16mib writes a relic of %d parts whose live bodies total %d bytes; relic-edit-part-16mib replaces its last part, of %d bytes, with as many. relic-edit-body-1mib saves a new %d-byte body over the body relic's large part.\n",
		FullBodiesRelicParts(fixture), fixture.FullBodiesBytes, fixture.SmallBodyBytes, fixture.LargeBodyBytes)
	fmt.Fprintf(&out, "The history and diff scenarios read a relic of one %d-byte part saved %d times after it was created, each save rewriting %d lines.\n",
		fixture.HistoryBodyBytes, fixture.HistoryEdits, relicHistoryLinesPerEdit)
	fmt.Fprintf(&out, "relic-sync-merge-100 times the sync of a clone that saved line %d of each of %d relics' %d-byte, %d-line body after another clone saved line %d and published; relic-merge-repetitive-1mib does the same for one %d-byte body of repeated lines. Both run on a copy of the fixture with an origin of its own.\n",
		second, fixture.MergeRelics, fixture.MergeBodyBytes, fixture.MergeBodyLines, first, fixture.RepetitiveBodyBytes)
	fmt.Fprintln(&out, "relic-linked-to-part-hot and relic-show-linked-part-hot look up the first part of the first relic, which every other relic links to once.")
	fmt.Fprintln(&out, "Moves are written as several moves per commit, so storage per move is a lower bound.")

	fmt.Fprintln(&out, "\n| Fixture step | Untimed (ms) |")
	fmt.Fprintln(&out, "| --- | ---: |")
	for _, name := range relicSortedKeys(report.FixtureMilliseconds) {
		fmt.Fprintf(&out, "| %s | %.0f |\n", name, report.FixtureMilliseconds[name])
	}

	fmt.Fprintf(&out, "\nPart positions after the fixture's moves: longest stored position %d bytes.\n", report.StoredLongestPositionBytes)
	fmt.Fprintln(&out, "\n| Reorder pattern | Parts | Moves | Refused | First refused move | Longest position (bytes) | Median position (bytes) |")
	fmt.Fprintln(&out, "| --- | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, result := range report.Reorder {
		fmt.Fprintf(&out, "| %s | %d | %d | %d | %d | %d | %d |\n", result.Pattern, result.Parts, result.Moves,
			result.Refused, result.FirstRefusedMove, result.LongestPositionBytes, result.MedianPositionBytes)
	}

	fmt.Fprintln(&out, "\n| Scenario | Git commands (highest per sample) |")
	fmt.Fprintln(&out, "| --- | --- |")
	for _, name := range relicSortedKeys(report.GitCommands) {
		if !strings.HasPrefix(name, "relic-linked-to-") && !strings.HasPrefix(name, "relic-refresh-") &&
			!strings.Contains(name, "-merge-") && name != "relic-fetch-100" &&
			name != "task-show-linked-hot" && name != "relic-show-linked-part-hot" {
			continue
		}
		commands := report.GitCommands[name]
		parts := make([]string, 0, len(commands))
		for _, command := range relicSortedKeys(commands) {
			parts = append(parts, fmt.Sprintf("%s %d", command, commands[command]))
		}
		if len(parts) == 0 {
			parts = append(parts, "none")
		}
		fmt.Fprintf(&out, "| %s | %s |\n", name, strings.Join(parts, ", "))
	}

	before := classAccounts(report.StorageBefore)
	after := classAccounts(report.StorageAfter)
	fmt.Fprintln(&out, "\n| Object class | Objects before | Raw bytes before | Objects after | Raw bytes after |")
	fmt.Fprintln(&out, "| --- | ---: | ---: | ---: | ---: |")
	for _, class := range objectClassOrder {
		fmt.Fprintf(&out, "| %s | %d | %d | %d | %d |\n", class,
			before[class].Objects, before[class].RawBytes, after[class].Objects, after[class].RawBytes)
	}
	fmt.Fprintf(&out, "| **Object directory bytes** | | %d | | %d |\n",
		report.StorageBefore.ObjectDirectoryBytes, report.StorageAfter.ObjectDirectoryBytes)
	fmt.Fprintln(&out, "\nBoth figures are the main repository, packed. Before is after the relics are created; after adds the links, the moves and the few small edits relic-edit-part-large and relic-move-part-large make. The scenarios that create relics run in a copy, so after minus before holds no created relic.")
	fmt.Fprintf(&out, "\nDisposable cache: %d bytes (projection %d, validation %d).\n",
		report.Cache.TotalBytes, report.Cache.ProjectionBytes, report.Cache.ValidationBytes)

	fmt.Fprintf(&out, "\nTask scenarios without and with the relic set. %s synchronize with a local bare origin, so they appear twice: \"origin refs loose\" is origin as a push leaves it, every ref a loose file; \"origin refs packed\" is origin after `git pack-refs --all`, as a hosted or maintained origin keeps it. Both sides are copies of a checkout that `git pack-refs --all` and `git gc` were run on last, so they differ by the relic set and nothing else. Git reads every ref a checkout holds, and the commit at its tip, when it fetches and pushes; a checkout written to since its last `git gc` holds loose refs and loose commits, which cost a synchronizing command more whether they belong to tasks or to relics. The task shown and edited has a typical number of relic links; task-show-linked-hot above shows the task every relic links to.\n",
		strings.Join(relicSyncingTaskScenarios, " and "))
	fmt.Fprintln(&out, "\n| Task scenario | Median without (ms) | Median with (ms) | P95 Git processes without | P95 Git processes with |")
	fmt.Fprintln(&out, "| --- | ---: | ---: | ---: | ---: |")
	writeComparisons := func(comparisons []RelicTaskComparison, label func(string) string) {
		for _, comparison := range comparisons {
			fmt.Fprintf(&out, "| %s | %.2f | %.2f | %d | %d |\n", label(comparison.Name),
				comparison.Without.MedianMilliseconds, comparison.With.MedianMilliseconds,
				comparison.Without.P95GitProcesses, comparison.With.P95GitProcesses)
		}
	}
	writeComparisons(report.TaskScenarios, func(name string) string {
		if slices.Contains(relicSyncingTaskScenarios, name) {
			return name + ", origin refs loose"
		}
		return name
	})
	writeComparisons(report.PackedOriginTaskScenarios, func(name string) string { return name + ", origin refs packed" })
	_, err := io.WriteString(w, out.String())
	return err
}

func classAccounts(account GitStorageAccount) map[string]ObjectClassAccount {
	accounts := make(map[string]ObjectClassAccount, len(account.Classes))
	for _, class := range account.Classes {
		accounts[class.Class] = class
	}
	return accounts
}

func relicSortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
