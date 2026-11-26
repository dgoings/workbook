package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

// Mutation witness: accepting a task fixture flag beside --relics would let
// an operator believe the relic run measured a fixture it never built.
func TestRelicsFlagRefusesOtherModes(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	refused := map[string][]string{
		"scaling":           {"--scaling"},
		"storage-resources": {"--storage-resources"},
		"scenario":          {"--scenario", "cli-list"},
		"scaling-point":     {"--scaling-point", "10x4"},
		"tasks":             {"--tasks", "50"},
		"tombstones":        {"--tombstones", "2"},
		"operations":        {"--operations", "5"},
	}
	for flag, extra := range refused {
		code, stderr := runRelicFlags(t, binary, append([]string{"--relics"}, extra...)...)
		want := "--" + flag + " is not valid with --relics; the relic scale defines its own fixture"
		if code != invocationExitCode || !strings.Contains(stderr, want) {
			t.Fatalf("--relics with --%s: exit %d, stderr %q; want %d and %q", flag, code, stderr, invocationExitCode, want)
		}
	}
	for _, args := range [][]string{
		{"--relics", "--relic-scale", "huge"},
		{"--relic-scale", "smoke"},
	} {
		if code, stderr := runRelicFlags(t, binary, args...); code != invocationExitCode || !strings.Contains(stderr, "--relic-scale") {
			t.Fatalf("%v: exit %d, stderr %q; want %d naming --relic-scale", args, code, stderr, invocationExitCode)
		}
	}
}

func runRelicFlags(t *testing.T, binary string, extra ...string) (int, string) {
	t.Helper()
	outputRoot := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := append([]string{
		"--workbook", binary,
		"--output-json", filepath.Join(outputRoot, "report.json"),
		"--output-markdown", filepath.Join(outputRoot, "report.md"),
	}, extra...)
	code := runWithBenchmark(context.Background(), args, &stdout, &stderr, func(context.Context, options) (perf.Report, error) {
		t.Fatal("the benchmark ran for refused flags")
		return perf.Report{}, nil
	})
	return code, stderr.String()
}

// Mutation witness: a relic run that skips a scenario, leaves the reorder or
// storage accounts out, lets the reverse lookup read relic history from Git,
// or loses the task comparison reports less than the story's table asks for.
func TestRunRelicBenchmarkSmokeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end relic benchmark run is slow")
	}
	started := time.Now()
	workbookBinary := filepath.Join(t.TempDir(), "workbook")
	buildWorkbookBinaryWithCommit(t, workbookBinary, "0000000000000000000000000000000000000000")
	outputRoot := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := run(context.Background(), []string{
		"--workbook", workbookBinary,
		"--relics", "--relic-scale", "smoke",
		"--samples", "1",
		"--timeout", "60s",
		"--output-json", filepath.Join(outputRoot, "report.json"),
		"--output-markdown", filepath.Join(outputRoot, "report.md"),
	}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("relic run exit code = %d; stdout = %q; stderr = %q", exitCode, stdout.String(), stderr.String())
	}
	t.Logf("relic smoke run took %s", time.Since(started))

	jsonData, err := os.ReadFile(filepath.Join(outputRoot, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report perf.Report
	if err := json.Unmarshal(jsonData, &report); err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]string{}
	for _, scenario := range report.Scenarios {
		outcomes[scenario.Name] = scenario.Outcome
	}
	for _, name := range perf.RelicScenarioNames() {
		if outcomes[name] != "completed" {
			t.Fatalf("scenario %s outcome = %q, want completed", name, outcomes[name])
		}
	}
	relics := report.Relics
	if relics == nil {
		t.Fatal("report has no relics section")
	}
	if len(relics.Reorder) != 3 {
		t.Fatalf("reorder results = %d, want 3", len(relics.Reorder))
	}
	// The reverse lookup reads no relic history: no rev-list or ls-tree, and
	// no more cat-file than a warm relic list, whose one cat-file reads the
	// project identity every command reads before it opens the index.
	hot := relics.GitCommands["relic-linked-to-hot"]
	for _, command := range []string{"rev-list", "ls-tree"} {
		if _, found := hot[command]; found {
			t.Fatalf("relic-linked-to-hot ran git %s: %v", command, hot)
		}
	}
	if listed := relics.GitCommands["relic-list"]; hot["cat-file"] > listed["cat-file"] {
		t.Fatalf("relic-linked-to-hot ran cat-file %d times, a warm relic list %d: %v", hot["cat-file"], listed["cat-file"], hot)
	}
	t.Logf("relic-linked-to-hot git commands: %v; relic-list: %v", hot, relics.GitCommands["relic-list"])
	if relics.StorageAfter.ObjectDirectoryBytes <= relics.StorageBefore.ObjectDirectoryBytes {
		t.Fatalf("storage after %d bytes, before %d", relics.StorageAfter.ObjectDirectoryBytes, relics.StorageBefore.ObjectDirectoryBytes)
	}
	var compared, want []string
	for _, comparison := range relics.TaskScenarios {
		compared = append(compared, comparison.Name)
	}
	for _, name := range perf.ScenarioNames() {
		if strings.HasPrefix(name, "cli-") {
			want = append(want, name)
		}
	}
	sort.Strings(compared)
	sort.Strings(want)
	if strings.Join(compared, ",") != strings.Join(want, ",") {
		t.Fatalf("task comparison = %v, want %v", compared, want)
	}
	markdown, err := os.ReadFile(filepath.Join(outputRoot, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "\n## Relics\n") {
		t.Fatal("Markdown report has no ## Relics section")
	}
}

// Mutation witness: keeping the 60-second default for --relics lets the
// documented command time out on relic-validate-full, which takes minutes at
// full scale; letting the relic default override an explicit --timeout would
// take the operator's choice away.
func TestRelicsDefaultTimeoutIsFiveMinutes(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	timeoutFor := func(extra ...string) time.Duration {
		t.Helper()
		outputRoot := t.TempDir()
		var stdout, stderr bytes.Buffer
		var got time.Duration
		args := append([]string{
			"--workbook", binary,
			"--output-json", filepath.Join(outputRoot, "report.json"),
			"--output-markdown", filepath.Join(outputRoot, "report.md"),
		}, extra...)
		runWithBenchmark(context.Background(), args, &stdout, &stderr, func(_ context.Context, options options) (perf.Report, error) {
			got = options.timeout
			return perf.Report{}, context.Canceled
		})
		if got == 0 {
			t.Fatalf("%v: the benchmark did not run; stderr %q", extra, stderr.String())
		}
		return got
	}
	for _, test := range []struct {
		args []string
		want time.Duration
	}{
		{[]string{"--relics"}, 300 * time.Second},
		{[]string{"--relics", "--timeout", "45s"}, 45 * time.Second},
		{[]string{"--relics", "--timeout", "60s"}, 60 * time.Second},
		{[]string{"--scenario", "cli-list"}, 60 * time.Second},
	} {
		if got := timeoutFor(test.args...); got != test.want {
			t.Fatalf("%v: timeout = %s, want %s", test.args, got, test.want)
		}
	}
}
