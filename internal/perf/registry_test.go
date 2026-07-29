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
