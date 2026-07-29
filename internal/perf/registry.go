package perf

import (
	"fmt"
	"strings"
)

var scenarioRegistry = []string{
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

// ScenarioNames returns the complete ordered benchmark scenario registry.
func ScenarioNames() []string {
	return append([]string(nil), scenarioRegistry...)
}

// ResolveScenarios validates requested scenario names and returns them in the
// registry's stable order. An omitted selection includes every registered
// scenario.
func ResolveScenarios(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return ScenarioNames(), nil
	}

	selected := make(map[string]struct{}, len(requested))
	for _, name := range requested {
		if _, duplicate := selected[name]; duplicate {
			return nil, fmt.Errorf("duplicate scenario %q", name)
		}
		selected[name] = struct{}{}
	}

	resolved := make([]string, 0, len(requested))
	for _, name := range scenarioRegistry {
		if _, wanted := selected[name]; wanted {
			resolved = append(resolved, name)
			delete(selected, name)
		}
	}
	if len(selected) != 0 {
		for _, name := range requested {
			if _, unknown := selected[name]; unknown {
				return nil, fmt.Errorf("unknown scenario %q; valid scenarios: %s", name, strings.Join(scenarioRegistry, ", "))
			}
		}
	}
	return resolved, nil
}
