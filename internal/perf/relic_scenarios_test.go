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
	// The plan's 21, and the four that measure the 1 MiB body save and the
	// relic at the 16 MiB live-body limit.
	if len(RelicScenarioNames()) != 25 {
		t.Fatalf("relic scenarios = %d, want 25", len(RelicScenarioNames()))
	}
	for _, name := range []string{"relic-edit-body-1mib", "relic-create-16mib", "relic-show-16mib", "relic-edit-part-16mib"} {
		if !slices.Contains(RelicScenarioNames(), name) {
			t.Fatalf("relic scenarios %v lack %s", RelicScenarioNames(), name)
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
