package perf

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSummarizeUsesNearestRankP95AndRetainsTimeouts(t *testing.T) {
	samples := []Sample{
		{Duration: 10 * time.Millisecond, GitProcesses: 2},
		{Duration: 20 * time.Millisecond, GitProcesses: 3},
		{Duration: 30 * time.Millisecond, GitProcesses: 4},
		{Duration: 40 * time.Millisecond, GitProcesses: 5},
		{TimedOut: true, Error: "timed out after 1s"},
	}
	got := Summarize(samples)
	if got.Completed != 4 || got.TimedOut != 1 {
		t.Fatalf("summary counts = %#v", got)
	}
	if got.P95Milliseconds != 40 || got.P95GitProcesses != 5 {
		t.Fatalf("summary p95 = %#v", got)
	}
}

func TestSummarizeRetainsGitProcessCountsFromTimedOutAndFailedSamples(t *testing.T) {
	samples := []Sample{
		{Duration: 10 * time.Millisecond, GitProcesses: 2},
		{Duration: 20 * time.Millisecond, GitProcesses: 3},
		{Duration: 30 * time.Millisecond, GitProcesses: 77, ExitCode: 1, Error: "product failed"},
		{Duration: time.Second, GitProcesses: 99, TimedOut: true, Error: "timed out"},
	}

	got := Summarize(samples)
	if got.Completed != 2 || got.TimedOut != 1 {
		t.Fatalf("summary counts = %#v", got)
	}
	if got.MinMilliseconds != 10 || got.MedianMilliseconds != 15 || got.P95Milliseconds != 20 {
		t.Fatalf("completed latency summary = %#v", got)
	}
	if got.P95GitProcesses != 99 {
		t.Fatalf("P95 Git processes = %d, want 99 from all observed samples", got.P95GitProcesses)
	}
}

func TestReportWritesVersionedJSONAndMarkdown(t *testing.T) {
	report := Report{
		Format: "workbook.performance-report", Version: 2, Phase: "baseline",
		Fixture:   FixtureSpec{TotalTasks: 500, ActiveTasks: 500, OperationsPerTask: 20, ObjectFormat: "sha1"},
		Targets:   Targets{WarmP95Milliseconds: 100, ColdP95Milliseconds: 200, BurstMilliseconds: 1000},
		Scenarios: []ScenarioResult{{Name: "cli-update", Surface: "cold-cli", Samples: []Sample{{Duration: 25 * time.Millisecond}}}},
	}
	var jsonOutput, markdownOutput bytes.Buffer
	if err := report.WriteJSON(&jsonOutput); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteMarkdown(&markdownOutput); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(jsonOutput.Bytes(), []byte(`"format":"workbook.performance-report"`)) {
		t.Fatalf("JSON = %s", jsonOutput.Bytes())
	}
	if !strings.Contains(markdownOutput.String(), "| cli-update | cold-cli |") {
		t.Fatalf("Markdown = %s", markdownOutput.String())
	}
}

// Mutation witnesses: evaluating duration with the maximum rather than p95,
// making a strict burst limit inclusive, or treating a zero Git-process limit
// as a budget each change the literal outcomes below.
func TestScenarioOutcomeDurationPolicies(t *testing.T) {
	tests := []struct {
		name    string
		target  ScenarioTarget
		samples []Sample
		want    string
	}{
		{
			name:    "p95 100 milliseconds is inclusive",
			target:  ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 100},
			samples: []Sample{{Duration: 100 * time.Millisecond, GitProcesses: 200}},
			want:    "pass",
		},
		{
			name:    "p95 200 milliseconds is inclusive",
			target:  ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 200},
			samples: []Sample{{Duration: 200 * time.Millisecond}},
			want:    "pass",
		},
		{
			name:   "one sample above p95 limit remains a pass",
			target: ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 100},
			samples: []Sample{
				{Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond},
				{Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond},
				{Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond},
				{Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond},
				{Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 100 * time.Millisecond}, {Duration: 101 * time.Millisecond},
			},
			want: "pass",
		},
		{
			name:    "burst exactly 1000 milliseconds misses",
			target:  ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 1000},
			samples: []Sample{{Duration: 1000 * time.Millisecond}},
			want:    "miss",
		},
		{
			name:    "burst below 1000 milliseconds passes",
			target:  ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 1000},
			samples: []Sample{{Duration: 999 * time.Millisecond}},
			want:    "pass",
		},
		{
			name:    "zero Git process limit is not a budget",
			target:  ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 100},
			samples: []Sample{{Duration: time.Millisecond, GitProcesses: 99}},
			want:    "pass",
		},
		{
			name:    "timeout beats failure and miss",
			target:  ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 100},
			samples: []Sample{{TimedOut: true}, {ExitCode: 1, Error: "failed"}, {Duration: 100 * time.Millisecond}},
			want:    "timeout",
		},
		{
			name:    "failure beats miss",
			target:  ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 100},
			samples: []Sample{{ExitCode: 1, Error: "failed"}, {Duration: 100 * time.Millisecond}},
			want:    "failed",
		},
		{
			name:    "miss beats pass",
			target:  ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 100},
			samples: []Sample{{Duration: time.Millisecond}, {Duration: 100 * time.Millisecond}},
			want:    "miss",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := scenarioOutcome(ScenarioResult{Target: &test.target, Samples: test.samples})
			if got != test.want {
				t.Fatalf("scenario outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReportWritesTargetPoliciesToJSONAndMarkdown(t *testing.T) {
	report := Report{Scenarios: []ScenarioResult{
		{
			Name: "cli-update", Surface: "cold-cli",
			Target:  &ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 200},
			Samples: []Sample{{Duration: time.Millisecond}},
		},
		{
			Name: "api-update", Surface: "warm-http",
			Target:  &ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 100},
			Samples: []Sample{{Duration: time.Millisecond}},
		},
		{
			Name: "cli-burst-independent-10", Surface: "cold-cli",
			Target:  &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 1000},
			Samples: []Sample{{Duration: time.Millisecond}},
		},
	}}
	var jsonOutput, markdownOutput bytes.Buffer
	if err := report.WriteJSON(&jsonOutput); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteMarkdown(&markdownOutput); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(jsonOutput.Bytes(), []byte(`"durationStatistic":"p95"`)) ||
		!bytes.Contains(jsonOutput.Bytes(), []byte(`"durationComparison":"less-than"`)) {
		t.Fatalf("JSON = %s", jsonOutput.Bytes())
	}
	for _, want := range []string{"p95 <= 200.00 ms", "p95 <= 100.00 ms", "each < 1000.00 ms"} {
		if !strings.Contains(markdownOutput.String(), want) {
			t.Fatalf("Markdown missing %q:\n%s", want, markdownOutput.String())
		}
	}
}

func TestReportNormalizesScenarioTargetOutcomes(t *testing.T) {
	target := &ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}
	report := Report{Scenarios: []ScenarioResult{
		{
			Name:    "pass",
			Target:  target,
			Samples: []Sample{{Duration: 2 * time.Second, GitProcesses: 19}},
		},
		{
			Name:    "process-miss",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, GitProcesses: 20}},
		},
		{
			Name:    "timeout",
			Target:  target,
			Samples: []Sample{{Duration: 60 * time.Second, TimedOut: true}},
		},
		{
			Name:    "failed",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, ExitCode: 4, Error: "corrupt"}},
		},
		{
			Name:    "later-timeout",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, GitProcesses: 1}, {Duration: time.Second, TimedOut: true}, {Duration: time.Second, GitProcesses: 1}},
		},
		{
			Name:    "later-failure",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, GitProcesses: 1}, {Duration: time.Second, ExitCode: 4, Error: "corrupt"}, {Duration: time.Second, GitProcesses: 1}},
		},
		{
			Name:    "later-miss",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, GitProcesses: 1}, {Duration: time.Second, GitProcesses: 20}, {Duration: time.Second, GitProcesses: 1}},
		},
		{
			Name:    "timeout-precedence-is-order-resistant",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, ExitCode: 4, Error: "corrupt"}, {Duration: time.Second, TimedOut: true}, {Duration: 3 * time.Second, GitProcesses: 20}},
		},
		{
			Name:    "miss-then-failure",
			Target:  target,
			Samples: []Sample{{Duration: 3 * time.Second, GitProcesses: 20}, {Duration: time.Second, ExitCode: 4, Error: "corrupt"}},
		},
		{
			Name:    "failure-then-miss",
			Target:  target,
			Samples: []Sample{{Duration: time.Second, ExitCode: 4, Error: "corrupt"}, {Duration: 3 * time.Second, GitProcesses: 20}},
		},
		{
			Name:    "local",
			Samples: []Sample{{Duration: time.Millisecond}},
		},
	}}

	normalized := report.normalized()
	got := make(map[string]string, len(normalized.Scenarios))
	for _, scenario := range normalized.Scenarios {
		got[scenario.Name] = scenario.Outcome
	}
	want := map[string]string{
		"pass":                                  "pass",
		"process-miss":                          "miss",
		"timeout":                               "timeout",
		"failed":                                "failed",
		"later-timeout":                         "timeout",
		"later-failure":                         "failed",
		"later-miss":                            "miss",
		"timeout-precedence-is-order-resistant": "timeout",
		"miss-then-failure":                     "failed",
		"failure-then-miss":                     "failed",
		"local":                                 "not-evaluated",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %#v, want %#v", got, want)
	}
}

func TestReportMarkdownShowsStrictProcessTargetAndOutcome(t *testing.T) {
	target := &ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationAtMost, MaxMilliseconds: 2000, MaxGitProcesses: 20}
	report := Report{
		Format: "workbook.performance-report",
		Scenarios: []ScenarioResult{{
			Name: "sync-small-changed-ref-set", Surface: "remote-sync",
			Target: target, Samples: []Sample{{Duration: time.Second, GitProcesses: 19}},
		}},
	}
	var output bytes.Buffer
	if err := report.WriteMarkdown(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "| sync-small-changed-ref-set | remote-sync |") ||
		!strings.Contains(output.String(), "| each <= 2000.00 ms | < 20 | pass |") {
		t.Fatalf("Markdown = %s", output.String())
	}
}
