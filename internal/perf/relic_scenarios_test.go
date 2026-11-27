package perf

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Mutation witness: registering a relic scenario would put the full relic
// fixture, minutes of setup, into every default benchmark run.
func TestRelicScenariosAreNotInTheDefaultRegistry(t *testing.T) {
	t.Parallel()
	registered := map[string]struct{}{}
	for _, name := range ScenarioNames() {
		registered[name] = struct{}{}
	}
	for _, name := range RelicScenarioNames() {
		if _, found := registered[name]; found {
			t.Fatalf("relic scenario %s is in the default registry", name)
		}
	}
	resolved, err := ResolveScenarios(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved, ScenarioNames()) {
		t.Fatalf("ResolveScenarios(nil) = %v, want the registry", resolved)
	}
	// The plan's 21, the four that measure the 1 MiB body save and the
	// relic at the 16 MiB live-body limit, the three that measure history
	// and diffs, and the two that look up what links to a part.
	if len(RelicScenarioNames()) != 30 {
		t.Fatalf("relic scenarios = %d, want 30", len(RelicScenarioNames()))
	}
	after := slices.Index(RelicScenarioNames(), "relic-body-1mib")
	if want := []string{"relic-history-100", "relic-diff-latest-100kib", "relic-diff-first-last-100kib"}; !slices.Equal(RelicScenarioNames()[after+1:after+4], want) {
		t.Fatalf("relic scenarios after relic-body-1mib = %v, want %v", RelicScenarioNames()[after+1:after+4], want)
	}
	for _, name := range []string{"relic-edit-body-1mib", "relic-create-16mib", "relic-show-16mib", "relic-edit-part-16mib"} {
		if !slices.Contains(RelicScenarioNames(), name) {
			t.Fatalf("relic scenarios %v lack %s", RelicScenarioNames(), name)
		}
	}
}

// Mutation witness: a part lookup that is not registered, is not measured,
// drops out of the report's reverse-lookup table or accepts a wrong count
// would leave the 200 ms reverse-lookup budget unchecked for parts.
func TestRelicScenariosIncludePartLookups(t *testing.T) {
	t.Parallel()
	names := []string{"relic-linked-to-part-hot", "relic-show-linked-part-hot"}
	for _, name := range names {
		if !slices.Contains(RelicScenarioNames(), name) {
			t.Fatalf("relic scenarios %v lack %s", RelicScenarioNames(), name)
		}
	}

	shared := sharedSmokeRelicFixtureFor(t)
	fixture := shared
	fixture.Root = filepath.Join(t.TempDir(), "fixture")
	if err := os.CopyFS(fixture.Root, os.DirFS(shared.Root)); err != nil {
		t.Fatal(err)
	}
	run := &relicRun{
		ctx:          context.Background(),
		spec:         RelicRunSpec{RunSpec: RunSpec{WorkbookBinary: buildWorkbookBinary(t), Samples: 1, CommandTimeout: 120 * time.Second}},
		dependencies: relicDependencies{measureCommand: MeasureCommandOutput},
		environment:  []string{"XDG_CONFIG_HOME=" + filepath.Join(t.TempDir(), "config")},
		results:      map[string]*ScenarioResult{},
		gitCommands:  map[string]map[string]int{},
	}
	measured := map[string]bool{}
	for _, read := range relicReverseLookups(fixture) {
		if !slices.Contains(names, read.name) {
			continue
		}
		if err := run.warm(read.name, fixture.Root, read.args, read.verify); err != nil {
			t.Fatal(err)
		}
		if sample := run.results[read.name].Samples[0]; !sampleSucceeded(sample) {
			t.Fatalf("%s failed: exit code %d: %s", read.name, sample.ExitCode, sample.Error)
		}
		measured[read.name] = true
		// A verifier that passes any answer would pass the wrong fixture.
		wrong := fixture
		wrong.HotRelicPartLinks++
		for _, other := range relicReverseLookups(wrong) {
			if other.name != read.name {
				continue
			}
			if _, err := run.untimed(other.name, fixture.Root, other.args, other.verify); err == nil {
				t.Fatalf("%s accepted %d links to the hot part", other.name, wrong.HotRelicPartLinks)
			}
		}
	}
	if len(measured) != len(names) {
		t.Fatalf("measured %v, want %v", measured, names)
	}

	var markdown strings.Builder
	report := &RelicBenchmarkReport{Fixture: fixture.Spec, GitCommands: run.gitCommands}
	if err := writeRelicMarkdown(&markdown, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !strings.Contains(markdown.String(), "| "+name+" | ") {
			t.Fatalf("Markdown lacks the Git commands of %s:\n%s", name, markdown.String())
		}
	}
}

// fakeRelicRun is a relic run whose measured command answers with stdout.
func fakeRelicRun(stdout string) *relicRun {
	return &relicRun{
		ctx:  context.Background(),
		spec: RelicRunSpec{RunSpec: RunSpec{WorkbookBinary: "workbook", Samples: 1, CommandTimeout: time.Second}},
		dependencies: relicDependencies{measureCommand: func(context.Context, CommandSpec) CommandMeasurement {
			return CommandMeasurement{
				Sample:      Sample{ExitCode: 0, Duration: time.Millisecond, GitProcesses: 1},
				Stdout:      []byte(stdout),
				GitCommands: map[string]int{"for-each-ref": 1},
			}
		}},
		results:     map[string]*ScenarioResult{},
		gitCommands: map[string]map[string]int{},
	}
}

// Mutation witness: a scenario that times a command without checking what it
// answered would report the time of a wrong answer as the time of the work.
func TestRelicScenariosCheckTheirOutput(t *testing.T) {
	t.Parallel()
	listOf := func(count int) string {
		items := make([]string, count)
		for index := range items {
			items[index] = `{"id":"WB-R-x"}`
		}
		return `{"format":"workbook.result","version":1,"command":"relic list","data":[` + strings.Join(items, ",") + `]}`
	}
	result := func(command, data string) string {
		return `{"format":"workbook.result","version":1,"command":"` + command + `","data":` + data + `}`
	}
	historyOf := func(changes, versions int) string {
		list := func(count int) string { return "[" + strings.TrimSuffix(strings.Repeat("{},", count), ",") + "]" }
		return result("relic show", `{"changes":`+list(changes)+`,"bodyVersions":`+list(versions)+`}`)
	}
	diffOf := func(parts string) string { return result("relic diff", `{"parts":[`+parts+`]}`) }
	// changed is an exact body diff with that many deleted and inserted lines,
	// split over two hunks beside a context line.
	changed := func(deleted, inserted int) string {
		lines := []string{`{"kind":"equal"}`}
		lines = append(lines, slices.Repeat([]string{`{"kind":"delete"}`}, deleted)...)
		inserts := slices.Repeat([]string{`{"kind":"insert"}`}, inserted)
		return `{"kind":"changed","body":{"text":true,"hunks":[{"lines":[` + strings.Join(lines, ",") +
			`]},{"lines":[` + strings.Join(inserts, ",") + `]}]}}`
	}
	exact := changed(5, 5)
	latest, firstLast := verifyRelicBodyDiff(5), verifyRelicBodyDiff(50)
	tests := []struct {
		name   string
		stdout string
		verify relicVerifier
	}{
		{"relic-list", listOf(21), verifyRelicListCount(22)},
		{"relic-rebuild-fresh-clone", `{"format":"workbook.result","version":1,"command":"rebuild","data":{"taskCount":10,"relicCount":21}}`, verifyRebuildRelicCount(22)},
		{"relic-body-2kib", strings.Repeat("x", 2047), verifyBodyLength(2048)},
		{"relic-create-16mib", `{"format":"workbook.result","version":1,"command":"relic create","data":{"changed":true,"parts":[{"body":{"bytes":10}},{"body":{"bytes":5}}]}}`, verifyRelicLiveBodies(2, 16)},
		{"relic-show-16mib", `{"format":"workbook.result","version":1,"command":"relic show","data":{"parts":[{"body":{"bytes":10}},{"body":{"bytes":6}},{"body":{"bytes":1}}]}}`, verifyRelicLiveBodies(2, 16)},
		{"relic-history-100", historyOf(10, 11), verifyRelicHistory(11)},
		{"relic-history-100", historyOf(11, 10), verifyRelicHistory(11)},
		{"relic-diff-latest-100kib", diffOf(""), latest},
		{"relic-diff-latest-100kib", diffOf(exact + "," + exact), latest},
		{"relic-diff-latest-100kib", diffOf(`{"kind":"added","body":{"text":true,"hunks":[{}]}}`), latest},
		{"relic-diff-latest-100kib", diffOf(`{"kind":"changed"}`), latest},
		{"relic-diff-first-last-100kib", diffOf(`{"kind":"changed","body":{"text":false}}`), firstLast},
		{"relic-diff-first-last-100kib", diffOf(`{"kind":"changed","body":{"text":true}}`), firstLast},
		{"relic-diff-first-last-100kib", diffOf(`{"kind":"changed","body":{"text":true,"approximate":true,"hunks":[{}]}}`), firstLast},
		{"relic-diff-latest-100kib", diffOf(changed(6, 5)), latest},
		{"relic-diff-latest-100kib", diffOf(changed(5, 6)), latest},
		{"relic-diff-latest-100kib", diffOf(changed(0, 1)), latest},
		{"relic-diff-latest-100kib", diffOf(changed(1, 0)), latest},
		{"relic-diff-first-last-100kib", diffOf(changed(51, 50)), firstLast},
		{"relic-diff-first-last-100kib", diffOf(changed(50, 51)), firstLast},
	}
	for _, test := range tests {
		run := fakeRelicRun(test.stdout)
		_, err := run.measure(test.name, t.TempDir(), []string{"relic", "list", "--json"}, test.verify)
		if err == nil || !strings.Contains(err.Error(), test.name) {
			t.Fatalf("%s: error = %v, want a failure naming the scenario", test.name, err)
		}
	}

	// The live-body total is the right answer for a create and a show.
	for _, stdout := range []string{
		`{"format":"workbook.result","version":1,"command":"relic create","data":{"changed":true,"parts":[{"body":{"bytes":10}},{"body":{"bytes":6}}]}}`,
		`{"format":"workbook.result","version":1,"command":"relic show","data":{"parts":[{"body":{"bytes":10}},{"body":{"bytes":6}}]}}`,
	} {
		if err := verifyRelicLiveBodies(2, 16)([]byte(stdout)); err != nil {
			t.Fatalf("%s: %v", stdout, err)
		}
	}

	if err := verifyRelicHistory(11)([]byte(historyOf(11, 11))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		stdout string
		verify relicVerifier
	}{{diffOf(exact), latest}, {diffOf(changed(1, 1)), latest}, {diffOf(changed(50, 50)), firstLast}} {
		if err := test.verify([]byte(test.stdout)); err != nil {
			t.Fatal(err)
		}
	}

	// The right answer passes, records its sample and keeps the Git command
	// tally.
	run := fakeRelicRun(listOf(22))
	if _, err := run.measure("relic-list", t.TempDir(), []string{"relic", "list", "--json"}, verifyRelicListCount(22)); err != nil {
		t.Fatal(err)
	}
	if got := len(run.results["relic-list"].Samples); got != 1 {
		t.Fatalf("samples = %d, want 1", got)
	}
	if run.gitCommands["relic-list"]["for-each-ref"] != 1 {
		t.Fatalf("git commands = %v", run.gitCommands["relic-list"])
	}

	// A command that failed is kept for the report, which fails the run, and
	// is not checked: there is no answer to check.
	failed := fakeRelicRun("")
	failed.dependencies.measureCommand = func(context.Context, CommandSpec) CommandMeasurement {
		return CommandMeasurement{Sample: Sample{ExitCode: 1, Error: "boom"}}
	}
	if _, err := failed.measure("relic-list", t.TempDir(), []string{"relic", "list", "--json"}, verifyRelicListCount(22)); err != nil {
		t.Fatalf("a failed command returned %v, want it retained", err)
	}
	if failed.results["relic-list"].Samples[0].ExitCode != 1 {
		t.Fatal("the failed sample was not retained")
	}
}

// Mutation witness: an origin left loose when the run asks for it packed, or
// packed when it does not, would report one origin's cost under the other's
// label.
func TestScenarioOriginIsPackedOnlyWhenAsked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const timeout = 30 * time.Second
	for _, packed := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "fixture")
		if _, _, err := runRepositoryGit(ctx, timeout, "", "init", "--quiet", root); err != nil {
			t.Fatal(err)
		}
		if err := configureFixtureRepository(ctx, root); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"commit", "--quiet", "--allow-empty", "-m", "task"},
			{"update-ref", "refs/workbook/tasks/a", "HEAD"},
			{"update-ref", "refs/workbook/relics/b", "HEAD"},
		} {
			if _, _, err := runRepositoryGit(ctx, timeout, root, args...); err != nil {
				t.Fatal(err)
			}
		}
		if err := publishScenarioOrigin(ctx, scenarioDependencies{packOrigin: packed}, timeout, root); err != nil {
			t.Fatal(err)
		}
		origin := filepath.Join(root, "benchmark-origin.git")
		format, _, err := runRepositoryGit(ctx, timeout, origin, "rev-parse", "--show-ref-format")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(format)) != "files" {
			t.Fatalf("origin ref format = %q; this test reads packed-refs, which only the files format writes", format)
		}
		packedRefs, err := os.ReadFile(filepath.Join(origin, "packed-refs"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, ref := range []string{"refs/workbook/tasks/a", "refs/workbook/relics/b"} {
			if got := strings.Contains(string(packedRefs), ref); got != packed {
				t.Fatalf("packOrigin %v: %s in packed-refs = %v; packed-refs %q", packed, ref, got, packedRefs)
			}
		}
	}
}

// Mutation witness: a report that drops the packed-origin rows, or prints
// them without saying which origin each table measured, leaves a reader
// unable to tell the two costs apart.
func TestRelicMarkdownLabelsTheOriginOfEachTaskComparison(t *testing.T) {
	t.Parallel()
	summary := func(median float64) Summary { return Summary{MedianMilliseconds: median} }
	report := &RelicBenchmarkReport{
		Fixture: SmokeRelicFixtureSpec(),
		TaskScenarios: []RelicTaskComparison{
			{Name: "cli-list", Without: summary(1), With: summary(2)},
			{Name: "cli-next", Without: summary(3), With: summary(4)},
		},
		PackedOriginTaskScenarios: []RelicTaskComparison{
			{Name: "cli-next", Without: summary(5), With: summary(6)},
		},
	}
	var out strings.Builder
	if err := writeRelicMarkdown(&out, report); err != nil {
		t.Fatal(err)
	}
	markdown := out.String()
	for _, want := range []string{
		"| cli-list | 1.00 | 2.00 |",
		"| cli-next, origin refs loose | 3.00 | 4.00 |",
		"| cli-next, origin refs packed | 5.00 | 6.00 |",
		"`git pack-refs --all`",
		"holds no created relic",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("Markdown lacks %q:\n%s", want, markdown)
		}
	}
}
