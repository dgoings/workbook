package perf

import (
	"reflect"
	"strings"
	"testing"
)

func TestScenarioNamesReturnsStableDefensiveRegistry(t *testing.T) {
	want := []string{
		"cli-create",
		"cli-delete",
		"cli-depend",
		"cli-free",
		"cli-list",
		"cli-move",
		"cli-restore",
		"cli-update",
		"cli-burst-independent-10",
		"cli-burst-same-task-10",
		"api-update",
		"api-burst-independent-10",
		"api-burst-same-task-10",
		"projection-rebuild",
		"projection-refresh-unchanged",
		"projection-refresh-one-changed",
		"sync-initial-local-bare",
		"sync-unchanged-local-bare",
		"sync-fresh-checkout",
		"sync-initial-publication",
		"sync-already-synchronized",
		"sync-small-changed-ref-set",
		"sync-divergent-tips",
		"sync-malformed-local-tip",
		"sync-malformed-remote-tip",
		"validate-full-history",
		"validate-cached-unchanged",
		"validate-five-changed",
	}

	got := ScenarioNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scenario names = %#v, want %#v", got, want)
	}
	got[0] = "changed-by-caller"
	if next := ScenarioNames(); !reflect.DeepEqual(next, want) {
		t.Fatalf("scenario names after caller mutation = %#v, want %#v", next, want)
	}
}

func TestResolveScenariosUsesRegistryOrderAndRejectsInvalidSelectors(t *testing.T) {
	all := ScenarioNames()
	resolved, err := ResolveScenarios(nil)
	if err != nil {
		t.Fatalf("resolve omitted selection: %v", err)
	}
	if !reflect.DeepEqual(resolved, all) {
		t.Fatalf("omitted selection = %#v, want %#v", resolved, all)
	}
	resolved[0] = "changed-by-caller"
	if next, err := ResolveScenarios(nil); err != nil || !reflect.DeepEqual(next, all) {
		t.Fatalf("resolve omitted selection after caller mutation = %#v, %v; want %#v, nil", next, err, all)
	}

	resolved, err = ResolveScenarios([]string{"sync-fresh-checkout", "cli-update"})
	if err != nil {
		t.Fatalf("resolve unordered selection: %v", err)
	}
	if want := []string{"cli-update", "sync-fresh-checkout"}; !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolved selection = %#v, want %#v", resolved, want)
	}

	if _, err := ResolveScenarios([]string{"cli-update", "cli-update"}); err == nil || !strings.Contains(err.Error(), "duplicate scenario \"cli-update\"") {
		t.Fatalf("duplicate selection error = %v, want duplicate guidance", err)
	}

	if _, err := ResolveScenarios([]string{"not-a-scenario"}); err == nil ||
		!strings.Contains(err.Error(), "unknown scenario \"not-a-scenario\"") ||
		!strings.Contains(err.Error(), strings.Join(all, ", ")) {
		t.Fatalf("unknown selection error = %v, want valid names %q", err, strings.Join(all, ", "))
	}
}

// Mutation witnesses: assigning the warm 100 ms policy to a cold mutation,
// omitting a local target, or making a burst p95-based would all make at least
// one public scenario report the wrong policy.
func TestLocalScenarioResultsAttachApprovedDurationTargets(t *testing.T) {
	coldSingle := ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 200}
	warmUpdate := ScenarioTarget{DurationStatistic: DurationP95, DurationComparison: DurationAtMost, MaxMilliseconds: 100}
	burst := ScenarioTarget{DurationStatistic: DurationEverySample, DurationComparison: DurationLessThan, MaxMilliseconds: 1000}

	want := map[string]ScenarioTarget{
		"cli-create":               coldSingle,
		"cli-delete":               coldSingle,
		"cli-depend":               coldSingle,
		"cli-free":                 coldSingle,
		"cli-move":                 coldSingle,
		"cli-restore":              coldSingle,
		"cli-update":               coldSingle,
		"cli-burst-independent-10": burst,
		"cli-burst-same-task-10":   burst,
		"api-update":               warmUpdate,
		"api-burst-independent-10": burst,
		"api-burst-same-task-10":   burst,
	}
	results := append(coldCLIResults(1), warmHTTPResults(1)...)
	for _, result := range results {
		if result.Name == "cli-list" {
			// The read path deliberately has no approved budget; see
			// TestColdListScenarioHasNoApprovedDurationTarget.
			continue
		}
		expected, ok := want[result.Name]
		if !ok {
			t.Fatalf("unexpected local scenario %q", result.Name)
		}
		if result.Target == nil || *result.Target != expected {
			t.Fatalf("%s target = %#v, want %#v", result.Name, result.Target, expected)
		}
	}
}
