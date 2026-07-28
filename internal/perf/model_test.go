package perf

import (
	"bytes"
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
