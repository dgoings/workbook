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
		Format: "workbook.performance-report", Version: 3,
		Environment: Environment{WorkbookBinarySHA256: "abc123"},
		Fixture:     FixtureSpec{TotalTasks: 500, ActiveTasks: 500, OperationsPerTask: 20, ObjectFormat: "sha1"},
		Scenarios:   []ScenarioResult{{Name: "cli-update", Surface: "cold-cli", Samples: []Sample{{Duration: 25 * time.Millisecond}}}},
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
	if !bytes.Contains(jsonOutput.Bytes(), []byte(`"workbookBinarySha256":"abc123"`)) {
		t.Fatalf("JSON = %s, want measured binary SHA-256", jsonOutput.Bytes())
	}
	if !strings.Contains(markdownOutput.String(), "| cli-update | cold-cli |") {
		t.Fatalf("Markdown = %s", markdownOutput.String())
	}
	for _, forbidden := range []string{"Target", "budget", "Phase"} {
		if strings.Contains(markdownOutput.String(), forbidden) {
			t.Fatalf("Markdown contains threshold language %q:\n%s", forbidden, markdownOutput.String())
		}
	}
}

// Mutation witnesses: dropping the timeout precedence, ignoring a later
// failed sample, or classifying an empty scenario as completed each change
// the literal outcomes below.
func TestReportNormalizesScenarioOutcomes(t *testing.T) {
	report := Report{Scenarios: []ScenarioResult{
		{
			Name:    "completed",
			Samples: []Sample{{Duration: 2 * time.Second, GitProcesses: 19}},
		},
		{
			Name:    "timeout",
			Samples: []Sample{{Duration: 60 * time.Second, TimedOut: true}},
		},
		{
			Name:    "failed",
			Samples: []Sample{{Duration: time.Second, ExitCode: 4, Error: "corrupt"}},
		},
		{
			Name:    "later-timeout",
			Samples: []Sample{{Duration: time.Second, GitProcesses: 1}, {Duration: time.Second, TimedOut: true}, {Duration: time.Second, GitProcesses: 1}},
		},
		{
			Name:    "later-failure",
			Samples: []Sample{{Duration: time.Second, GitProcesses: 1}, {Duration: time.Second, ExitCode: 4, Error: "corrupt"}, {Duration: time.Second, GitProcesses: 1}},
		},
		{
			Name:    "timeout-precedence-is-order-resistant",
			Samples: []Sample{{Duration: time.Second, ExitCode: 4, Error: "corrupt"}, {Duration: time.Second, TimedOut: true}, {Duration: 3 * time.Second, GitProcesses: 20}},
		},
		{
			Name: "empty",
		},
		{
			Name:    "repository-success",
			Surface: "repository",
			Samples: []Sample{{Duration: time.Millisecond}},
		},
		{
			Name:    "repository-failure",
			Surface: "repository",
			Samples: []Sample{{Duration: time.Millisecond, ExitCode: 1, Error: "sync failed"}},
		},
		{
			Name:    "repository-timeout",
			Surface: "repository",
			Samples: []Sample{{Duration: time.Second, TimedOut: true, Error: "timed out"}},
		},
	}}

	normalized := report.normalized()
	got := make(map[string]string, len(normalized.Scenarios))
	for _, scenario := range normalized.Scenarios {
		got[scenario.Name] = scenario.Outcome
	}
	want := map[string]string{
		"completed":                             "completed",
		"timeout":                               "timeout",
		"failed":                                "failed",
		"later-timeout":                         "timeout",
		"later-failure":                         "failed",
		"timeout-precedence-is-order-resistant": "timeout",
		"empty":                                 "failed",
		"repository-success":                    "completed",
		"repository-failure":                    "failed",
		"repository-timeout":                    "timeout",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcomes = %#v, want %#v", got, want)
	}
}

func TestReportMarkdownShowsScenarioOutcome(t *testing.T) {
	report := Report{
		Format: "workbook.performance-report",
		Scenarios: []ScenarioResult{{
			Name: "sync-small-changed-ref-set", Surface: "remote-sync",
			Samples: []Sample{{Duration: time.Second, GitProcesses: 19}},
		}},
	}
	var output bytes.Buffer
	if err := report.WriteMarkdown(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "| sync-small-changed-ref-set | remote-sync |") ||
		!strings.Contains(output.String(), "| 19 | completed |") {
		t.Fatalf("Markdown = %s", output.String())
	}
}
