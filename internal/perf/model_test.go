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

func TestReportWritesVersionedJSONAndMarkdown(t *testing.T) {
	report := Report{
		Format: "workbook.performance-report", Version: 1, Phase: "baseline",
		Fixture:   FixtureSpec{ActiveTasks: 500, OperationsPerTask: 20, ObjectFormat: "sha1"},
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
		"local":                                 "not-evaluated",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %#v, want %#v", got, want)
	}
}

func TestReportMarkdownShowsStrictProcessTargetAndOutcome(t *testing.T) {
	target := &ScenarioTarget{MaxMilliseconds: 2000, MaxGitProcesses: 20}
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
		!strings.Contains(output.String(), "| 2000.00 | < 20 | pass |") {
		t.Fatalf("Markdown = %s", output.String())
	}
}
