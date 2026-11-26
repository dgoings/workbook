package perf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
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
	"relic-show-part-2kib",
	"relic-body-2kib",
	"relic-body-1mib",
	"relic-list",
	"relic-list-type",
	"relic-linked-to-hot",
	"relic-linked-to-typical",
	"task-show-linked-hot",
	"relic-push-100",
	"relic-fetch-100",
	"relic-refresh-0",
	"relic-refresh-1",
	"relic-refresh-100",
	"relic-clone-setup",
	"relic-rebuild-fresh-clone",
	"relic-first-read-fresh-clone",
	"relic-validate-full",
}

// RelicScenarioNames returns the relic family's scenarios in the order they
// run. The names are the story's, at every scale: at the smoke scale
// relic-body-1mib reads the smoke body and relic-push-100 pushes the smoke
// count.
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
	TaskScenarios              []RelicTaskComparison     `json:"taskScenarios"`
}

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
		return measurement, nil
	}
	if err := verify(measurement.Stdout); err != nil {
		return measurement, fmt.Errorf("%s: %w", name, err)
	}
	return measurement, nil
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

func (b *relicBench) relicCount() int { return b.run.spec.Relics.Relics + 2 }

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
		if err := initBareFixtureOrigin(ctx, spec.FixtureTimeout, spec.Fixture.ObjectFormat, b.origin); err != nil {
			return err
		}
		if _, _, err := runRepositoryGit(ctx, spec.FixtureTimeout, b.main, "remote", "add", "origin", b.origin); err != nil {
			return err
		}
		_, _, err := runRepositoryGit(ctx, spec.FixtureTimeout, b.main, "push", "--quiet", "origin", "refs/workbook/*:refs/workbook/*")
		return err
	}); err != nil {
		return err
	}
	b.cloneA = filepath.Join(b.root, "clone-a")
	b.cloneB = filepath.Join(b.root, "clone-b")
	return b.stage("clones", func(ctx context.Context) error {
		for _, clone := range []string{b.cloneA, b.cloneB} {
			if err := b.cloneOrigin(ctx, clone); err != nil {
				return err
			}
			if _, err := b.run.untimed("set up "+filepath.Base(clone), clone, []string{"setup", "--json"}, nil); err != nil {
				return err
			}
			if _, err := b.run.untimed("index "+filepath.Base(clone), clone, []string{"relic", "list", "--json"}, verifyRelicListCount(b.relicCount())); err != nil {
				return err
			}
		}
		return nil
	})
}

// cloneOrigin makes a plain clone of the origin with the fixture's isolation
// settings. A clone fetches no Workbook refs; setup does.
func (b *relicBench) cloneOrigin(ctx context.Context, destination string) error {
	if err := runFixtureGitInRoot(ctx, destination, "clone", "--quiet", b.origin, destination); err != nil {
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
	reads := []struct {
		name   string
		args   []string
		verify relicVerifier
	}{
		{"relic-show-part-2kib", []string{"relic", "show", small, "--json"}, verifyRelicShowParts(1, int64(relics.SmallBodyBytes))},
		{"relic-body-2kib", []string{"relic", "show", small, "--body"}, verifyBodyLength(relics.SmallBodyBytes)},
		{"relic-body-1mib", []string{"relic", "show", large, "--body"}, verifyBodyLength(relics.LargeBodyBytes)},
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
	plans := 0
	for index := range relics.Relics {
		if relicFixtureTypes[index%len(relicFixtureTypes)] == "plan" {
			plans++
		}
	}
	for _, read := range []struct {
		name   string
		args   []string
		verify relicVerifier
	}{
		{"relic-list", []string{"relic", "list", "--json"}, verifyRelicListCount(listed)},
		{"relic-list-type", []string{"relic", "list", "--type", "plan", "--json"}, verifyRelicListCount(plans)},
		{"relic-linked-to-hot", []string{"relic", "list", "--linked-to", fixture.HotTask, "--json"}, verifyRelicListCount(fixture.HotTaskLinks)},
		{"relic-linked-to-typical", []string{"relic", "list", "--linked-to", fixture.TypicalTask, "--json"}, verifyRelicListCount(fixture.TypicalTaskLinks)},
		{"task-show-linked-hot", []string{"show", fixture.HotTask, "--json"}, verifyTaskRelicLinks(fixture.HotTaskLinks)},
	} {
		for range samples {
			if err := run.warm(read.name, b.main, read.args, read.verify); err != nil {
				return err
			}
		}
	}
	return nil
}

// editInCloneB gives count fixture relics, starting at first, one body edit
// each in clone B, through the relic service in process, as a teammate's
// edits would arrive. It is setup: the push that publishes them is measured.
func (b *relicBench) editInCloneB(first, count int) error {
	ctx, cancel := context.WithTimeout(b.run.ctx, b.run.spec.FixtureTimeout)
	defer cancel()
	repository, err := gitstore.Open(ctx, b.cloneB)
	if err != nil {
		return err
	}
	service := core.RelicService{
		Config: b.fixture.Config,
		Reader: repository,
		Writer: repository,
		Tasks:  repository,
		IDs:    core.CryptoULIDSource{Now: time.Now},
		Actor:  benchmarkActorID,
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

// measureFreshClones sets up, rebuilds and first reads a new clone per sample.
func (b *relicBench) measureFreshClones() error {
	run := b.run
	for sample := range run.spec.Samples {
		clone := filepath.Join(b.root, fmt.Sprintf("fresh-%03d", sample+1))
		if err := b.stage(fmt.Sprintf("fresh-clone-%03d", sample+1), func(ctx context.Context) error {
			return b.cloneOrigin(ctx, clone)
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
// on a copy of the relic fixture, with the same spec and samples.
func (b *relicBench) compareTaskScenarios() error {
	var names []string
	for _, name := range scenarioRegistry {
		if strings.HasPrefix(name, "cli-") {
			names = append(names, name)
		}
	}
	base := scenarioDependencies{
		buildFixture:      BuildFixture,
		prepareProjection: prepareProjection,
		measureCommand:    b.run.dependencies.measureCommand,
		cleanupFixture:    os.RemoveAll,
	}
	var without, with []ScenarioResult
	if err := b.stage("task-scenarios-without", func(ctx context.Context) (err error) {
		without, err = runColdCLI(b.run.ctx, b.run.spec.RunSpec, filepath.Join(b.root, "tasks-without"), names, base)
		return err
	}); err != nil {
		return err
	}
	withRelics := base
	withRelics.buildFixture = func(_ context.Context, root string, _ FixtureSpec) (Fixture, error) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return Fixture{}, err
		}
		if err := os.MkdirAll(filepath.Dir(absRoot), 0o755); err != nil {
			return Fixture{}, err
		}
		if err := os.CopyFS(absRoot, os.DirFS(b.template)); err != nil {
			return Fixture{}, fmt.Errorf("copy the relic fixture: %w", err)
		}
		fixture := b.fixture.Fixture
		fixture.Root = absRoot
		return fixture, nil
	}
	if err := b.stage("task-scenarios-with", func(ctx context.Context) (err error) {
		with, err = runColdCLI(b.run.ctx, b.run.spec.RunSpec, filepath.Join(b.root, "tasks-with"), names, withRelics)
		return err
	}); err != nil {
		return err
	}
	for index, name := range names {
		for _, result := range []ScenarioResult{without[index], with[index]} {
			for _, sample := range result.Samples {
				if !sampleSucceeded(sample) {
					return fmt.Errorf("task comparison %s failed: exit code %d: %s", name, sample.ExitCode, sample.Error)
				}
			}
		}
		b.report.TaskScenarios = append(b.report.TaskScenarios, RelicTaskComparison{
			Name: name, Without: without[index].Summary, With: with[index].Summary,
		})
	}
	return nil
}

// writeRelicMarkdown writes the ## Relics section: the fixture, the reorder
// results, the Git commands of the reverse lookups and refreshes, storage
// before and after by class, and the task comparison.
func writeRelicMarkdown(w io.Writer, report *RelicBenchmarkReport) error {
	if report == nil {
		return nil
	}
	var out strings.Builder
	fixture := report.Fixture
	fmt.Fprintln(&out, "\n## Relics")
	fmt.Fprintf(&out, "\nFixture: %d relics × %d parts of %d bytes, %d links each; a large relic of %d parts × %d bytes; body parts of %d and %d bytes; %d moves in updates of %d; %d changed relics per sync; seed %d.\n",
		fixture.Relics, fixture.PartsPerRelic, fixture.BodyBytes, fixture.LinksPerRelic,
		fixture.LargeParts, fixture.LargePartBodyBytes, fixture.LargeBodyBytes, fixture.SmallBodyBytes,
		fixture.Moves, fixture.MovesPerUpdate, fixture.ChangedRelics, fixture.Seed)
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
		if !strings.HasPrefix(name, "relic-linked-to-") && !strings.HasPrefix(name, "relic-refresh-") && name != "task-show-linked-hot" {
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

	fmt.Fprintln(&out, "\n| Task scenario | Median without (ms) | Median with (ms) | P95 Git processes without | P95 Git processes with |")
	fmt.Fprintln(&out, "| --- | ---: | ---: | ---: | ---: |")
	for _, comparison := range report.TaskScenarios {
		fmt.Fprintf(&out, "| %s | %.2f | %.2f | %d | %d |\n", comparison.Name,
			comparison.Without.MedianMilliseconds, comparison.With.MedianMilliseconds,
			comparison.Without.P95GitProcesses, comparison.With.P95GitProcesses)
	}
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
