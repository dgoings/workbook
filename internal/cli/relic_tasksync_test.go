package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The tests in this file pin that a task command's automatic synchronization
// moves no relic ref, and that the relic commands, fetch, push and sync still
// do.

// trackedRelicHead is the tip the clone's tracking ref for a relic names, or
// "" when it has none.
func trackedRelicHead(repository, relicID string) string {
	output, err := exec.Command("git", "-C", repository, "rev-parse", "--verify", "--quiet",
		"refs/workbook/remotes/origin/relics/"+relicID).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// runTaskCommandWithSync runs a task command whose synchronization is on and
// requires it to exit 0 with a completed inline sync.
func runTaskCommandWithSync(t *testing.T, repository string, args ...string) relicSyncEnvelope {
	t.Helper()
	full := append(append([]string(nil), args...), "--json")
	code, stdout, stderr := run(t, repository, full...)
	if code != 0 {
		t.Fatalf("%v = (%d, %q, %q), want exit 0", full, code, stdout, stderr)
	}
	envelope := decodeRelicSyncEnvelope(t, stdout, args[0])
	if envelope.Sync == nil || !envelope.Sync.Enabled || envelope.Sync.Status != "completed" {
		t.Fatalf("%v sync = %+v, want a completed inline sync", full, envelope.Sync)
	}
	return envelope
}

// A relic another clone published stays on origin while this clone runs task
// commands, and arrives with a relic command, a fetch or a sync.
func TestATaskCommandLeavesOriginsRelicsThere(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"relic create": {"relic", "create", "Made here", "--type", "spec"},
		"fetch":        {"fetch"},
		"sync":         {"sync"},
	} {
		name, args := name, args
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			first, second := cliSyncRepositories(t)
			relicID := mustCreateRelic(t, first, "Published elsewhere", "--type", "spec").Relic.ID
			published := originRelicHead(t, first, relicID)
			if published == "" {
				t.Fatalf("relic create did not publish %s", relicID)
			}

			task := runTaskCommandWithSync(t, second, "create", "A task")
			var created core.Task
			if err := json.Unmarshal(task.Data, &created); err != nil {
				t.Fatal(err)
			}
			runTaskCommandWithSync(t, second, "update", created.ID, "--title", "Renamed task")
			if got := localRelicRef(second, relicID); got != "" {
				t.Fatalf("after task commands the clone holds the relic at %s", got)
			}
			if got := refsUnder(t, second, "refs/workbook/remotes/origin/relics/"); got != "" {
				t.Fatalf("after task commands the clone tracks relics:\n%s", got)
			}

			if code, stdout, stderr := run(t, second, args...); code != 0 {
				t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
			}
			if got := localRelicRef(second, relicID); got != published {
				t.Fatalf("after %s the relic is at %q, want %s", name, got, published)
			}
			if got := trackedRelicHead(second, relicID); got != published {
				t.Fatalf("after %s the relic's tracking ref is at %q, want %s", name, got, published)
			}
		})
	}
}

// A relic recorded with --no-sync stays local while this clone runs task
// commands, and is published by a push, a sync or a relic command on it.
func TestATaskCommandLeavesALocalRelicUnpublished(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"push", "sync", "relic update"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, second := cliSyncRepositories(t)
			relicID := mustCreateRelic(t, second, "Kept here", "--type", "spec", "--no-sync").Relic.ID

			task := runTaskCommandWithSync(t, second, "create", "A task")
			var created core.Task
			if err := json.Unmarshal(task.Data, &created); err != nil {
				t.Fatal(err)
			}
			runTaskCommandWithSync(t, second, "update", created.ID, "--title", "Renamed task")
			if got := originRelicHead(t, second, relicID); got != "" {
				t.Fatalf("origin holds the relic at %s after task commands", got)
			}

			args := []string{name}
			if name == "relic update" {
				args = []string{"relic", "update", relicID, "--title", "Published now"}
			}
			if code, stdout, stderr := run(t, second, args...); code != 0 {
				t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
			}
			if got, want := originRelicHead(t, second, relicID), relicHeadOf(t, second, relicID); got != want {
				t.Fatalf("origin holds %q after %s, want %s", got, name, want)
			}
		})
	}
}

// A relic body conflict waiting at origin is not met by a task command: it
// exits 0, reports no relic conflict, parks nothing and leaves the relic
// where it was. The next relic command or sync meets it as before.
func TestATaskCommandLeavesAWaitingRelicConflict(t *testing.T) {
	t.Parallel()
	taskCommands := map[string][]string{
		"create":       {"create", "x"},
		"status add":   {"status", "add", "icebox"},
		"priority add": {"priority", "add", "urgent", "--before", "high"},
		"key add":      {"key", "add", "NEWKEY"},
	}
	for name, args := range taskCommands {
		for _, after := range []string{"relic update", "sync"} {
			name, args, after := name, args, after
			t.Run(name+" then "+after, func(t *testing.T) {
				t.Parallel()
				state := newRelicBodyConflict(t, "Contested", "base\n", "theirs\n", "ours\n", "body.md")
				before := relicHeadOf(t, state.second, state.relicID)

				for _, jsonMode := range []bool{true, false} {
					command := append([]string(nil), args...)
					if jsonMode {
						command = append(command, "--json")
					} else if name != "create" {
						// The vocabulary commands refuse a second identical add.
						continue
					}
					code, stdout, stderr := run(t, state.second, command...)
					if code != 0 {
						t.Fatalf("%v = (%d, %q, %q), want exit 0", command, code, stdout, stderr)
					}
					if jsonMode {
						envelope := decodeRelicSyncEnvelope(t, stdout, name)
						if len(envelope.RelicConflict) != 0 {
							t.Fatalf("%v relicConflict = %+v, want none", command, envelope.RelicConflict)
						}
					} else if strings.Contains(stdout, "Relic conflict:") || strings.Contains(stderr, state.relicID) {
						t.Fatalf("%v names the relic conflict:\n%s\n%s", command, stdout, stderr)
					}
				}
				if got := refsUnder(t, state.second, "refs/workbook/reconciled-relics/"); got != "" {
					t.Fatalf("the task command parked a relic tip:\n%s", got)
				}
				if got := relicHeadOf(t, state.second, state.relicID); got != before {
					t.Fatalf("the task command moved the relic from %s to %s", before, got)
				}

				if after == "relic update" {
					stopped := runRelicJSON(t, state.second, 8, "update", state.relicID, "--title", "T")
					if len(stopped.RelicConflict) != 1 || stopped.RelicConflict[0].RelicID != state.relicID ||
						stopped.RelicConflict[0].Type != core.RelicConflictBody {
						t.Fatalf("relic update relicConflict = %+v, want the relic's body conflict", stopped.RelicConflict)
					}
					return
				}
				code, stdout, stderr := run(t, state.second, "sync")
				if code != 8 || !strings.Contains(stdout, "Relic conflict:\t"+state.relicID+"\tbody\t") {
					t.Fatalf("sync = (%d, %q, %q), want exit 8 naming the relic conflict", code, stdout, stderr)
				}
			})
		}
	}
}

// relicGitWork is every Git command line in cost that names a relic ref,
// namespace or refspec.
func relicGitWork(cost gitCost) []string {
	var named []string
	for _, line := range cost.lines {
		if strings.Contains(line, "relics") {
			named = append(named, line)
		}
	}
	return named
}

// The Git commands a task command runs name no relic ref, namespace or
// refspec, with relics on origin, in the checkout and on their tracking refs.
// The relic command beside them shows the trace would see one.
func TestATaskCommandsGitWorkNamesNoRelicRef(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	first, second := cliSyncRepositories(t)
	mustCreateRelic(t, first, "On origin", "--type", "spec")
	mustCreateRelic(t, second, "Here and on origin", "--type", "spec")
	mustCreateRelic(t, second, "Only here", "--type", "spec", "--no-sync")
	task := runTaskCommandWithSync(t, second, "create", "A task")
	var created core.Task
	if err := json.Unmarshal(task.Data, &created); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"create", "Traced", "--json"},
		{"update", created.ID, "--title", "Traced update", "--json"},
		{"next", "--json"},
		{"next", "--claim", "--json"},
		{"status", "add", "icebox", "--json"},
		{"list", "--json"},
	} {
		cost, _ := tracedCommand(t, binary, second, nil, args...)
		if named := relicGitWork(cost); len(named) > 0 {
			t.Errorf("%v runs Git naming relics:\n%s", args, strings.Join(named, "\n"))
		}
	}
	// show lists the relics linked to the task from what this checkout holds,
	// and contacts no remote.
	cost, _ := tracedCommand(t, binary, second, nil, "show", created.ID, "--json")
	if len(cost.connections) != 0 {
		t.Errorf("show connected to a remote: %v", cost.connections)
	}
	cost, _ = tracedCommand(t, binary, second, nil, "relic", "create", "Traced relic", "--type", "spec", "--json")
	if len(relicGitWork(cost)) == 0 {
		t.Fatalf("relic create names no relic ref; the trace is not seeing what it should:\n%s", strings.Join(cost.lines, "\n"))
	}
}

// A task command handed to a running watcher waits for no relic transfer: it
// runs no fetch or push and no Git command naming a relic. It still reports
// the relic conflict the watcher recorded, read from this checkout.
func TestATaskCommandHandedToAWatcherRunsNoRelicGitWork(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	state := newRelicBodyConflict(t, "Contested while watched", "base\n", "theirs\n", "ours\n", "body.md")
	output := startCLIWatcher(t, state.second, "1h")
	if !strings.Contains(output.String(), "conflict on relic "+state.relicID) {
		t.Fatalf("the watcher's terminal does not name the relic conflict:\n%s", output.String())
	}

	cost, stdout := tracedCommand(t, binary, state.second, nil, "create", "Handed over", "--json")
	envelope := decodeRelicSyncEnvelope(t, string(stdout), "create")
	if envelope.Sync == nil || envelope.Sync.Status != syncStatusDeferred {
		t.Fatalf("sync = %+v, want the create handed to the watcher", envelope.Sync)
	}
	if len(cost.connections) != 0 {
		t.Errorf("the handed create connected to a remote: %v", cost.connections)
	}
	if named := relicGitWork(cost); len(named) > 0 {
		t.Errorf("the handed create runs Git naming relics:\n%s", strings.Join(named, "\n"))
	}
	if len(envelope.RelicConflict) != 1 || envelope.RelicConflict[0].RelicID != state.relicID {
		t.Errorf("relicConflict = %+v, want the conflict the watcher recorded", envelope.RelicConflict)
	}
}
