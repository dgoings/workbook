package core

import (
	"strings"
	"testing"
)

// Four separate tables in configop.go decide what happens to a priority
// operation: TouchesPriorities routes it to the priority section,
// configPriorities.apply decides what it does there, configOperationShapes
// says which members it may carry, and configOperationMinReader says what
// generation a reader needs to fold it. Adding a ninth priority operation
// means adding it to all four, and the compiler checks none of them — a map
// lookup that misses returns the zero value rather than failing to build.
//
// configOperationShapes is the one table an unlisted operation cannot hide
// from, because validation reads it for every operation of every type, so it
// stands in here for the set of types that exist. Each test below asks one
// question of that set.

// The routing table and the wire names agree in both directions. A priority
// operation that TouchesPriorities does not claim is folded into the status
// vocabulary instead, where it is refused as an unsupported type — so a
// project's first priority change would fail outright rather than quietly
// doing the wrong thing, which is the good case. The bad case is the reverse:
// gitstore asks this same question to decide whether a write is the moment to
// record the built-in priorities a project's existing tasks depend on, and a
// priority operation it does not recognize is a first priority change that
// lands without them, stranding every task in the project.
func TestPriorityOperationTypesAreRoutedByTheirWireName(t *testing.T) {
	for operationType := range configOperationShapes {
		named := strings.HasPrefix(string(operationType), "priority.")
		if routed := operationType.TouchesPriorities(); routed != named {
			t.Errorf("%q: TouchesPriorities() = %t, but its wire name says %t; "+
				"the routing switch and the operation's name have to agree",
				operationType, routed, named)
		}
	}
}

// Every priority operation raises the reader bar to generation 3.
//
// This is the table to get wrong quietly. configOperationMinReader is what
// stamps a pack with the generation a reader needs, and a type missing from
// it takes the zero value from the map lookup — so its pack ships claiming
// any build can fold it. An older build then reads a priorities section it
// has no member for, or folds an operation it does not understand as though
// it did, instead of telling its user to upgrade. Nothing fails loudly; the
// project is simply misread by half the team.
func TestPriorityOperationTypesRequireAGenerationThreeReader(t *testing.T) {
	for operationType := range configOperationShapes {
		if !operationType.TouchesPriorities() {
			continue
		}
		if generation := configOperationMinReader[operationType]; generation != 3 {
			t.Errorf("configOperationMinReader[%q] = %d, want 3; a priority operation that does not raise "+
				"the bar ships unstamped and is misfolded by builds that predate the priorities section",
				operationType, generation)
		}
	}
}

// The eight are all present. The two tests above are consistency checks — they
// pass vacuously if a type is dropped from configOperationShapes entirely, or
// if the priority section is somehow empty — so this one pins the membership
// itself, and fails when a ninth operation is added without a decision about
// the tables above.
func TestEveryBuiltInPriorityOperationTypeIsAccountedFor(t *testing.T) {
	want := []ConfigOperationType{
		ConfigPriorityAdd, ConfigPriorityRename, ConfigPriorityRelabel, ConfigPriorityRemove,
		ConfigPriorityReorder, ConfigPriorityTag, ConfigPriorityUntag, ConfigPriorityRecolor,
	}
	for _, operationType := range want {
		if _, ok := configOperationShapes[operationType]; !ok {
			t.Errorf("configOperationShapes has no entry for %q", operationType)
		}
	}
	found := 0
	for operationType := range configOperationShapes {
		if operationType.TouchesPriorities() {
			found++
		}
	}
	if found != len(want) {
		t.Errorf("configOperationShapes holds %d priority operations, want %d; a new one needs an entry in "+
			"TouchesPriorities, configPriorities.apply, configOperationShapes and configOperationMinReader",
			found, len(want))
	}
}
