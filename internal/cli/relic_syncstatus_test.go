package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// workbook sync --status lists the relic conflicts a running watcher holds,
// in text and in JSON, in the shape a later command reports them, with no
// body text: the watcher keeps none and the status reads none.
func TestSyncStatusListsTheRelicConflictsAWatcherHolds(t *testing.T) {
	t.Parallel()
	state := newRelicBodyConflict(t, "Contested while watched", "base\n", "theirs\n", "ours\n", "body.md")
	output := startCLIWatcher(t, state.second, "1h")
	if !strings.Contains(output.String(), "conflict on relic "+state.relicID) {
		t.Fatalf("the watcher's terminal does not name the relic conflict:\n%s", output.String())
	}

	code, stdout, stderr := run(t, state.second, "sync", "--status", "--json")
	if code != 0 {
		t.Fatalf("sync --status --json = (%d, %q, %q)", code, stdout, stderr)
	}
	var envelope struct {
		Data struct {
			Running        bool                 `json:"running"`
			RelicConflicts []core.RelicConflict `json:"relicConflicts"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode %s: %v", stdout, err)
	}
	if !envelope.Data.Running || len(envelope.Data.RelicConflicts) != 1 {
		t.Fatalf("status = %s, want a running watcher holding one relic conflict", stdout)
	}
	conflict := envelope.Data.RelicConflicts[0]
	if conflict.RelicID != state.relicID || conflict.Type != core.RelicConflictBody || conflict.Part != state.partID ||
		conflict.Body == nil || conflict.Body.Ours.Object == "" || conflict.Body.Theirs.Object == "" || conflict.ParkedRef == "" {
		t.Fatalf("relic conflict = %+v, want the body conflict on %s#%s", conflict, state.relicID, state.partID)
	}
	if conflict.Body.Ours.Text != nil || conflict.Body.Theirs.Text != nil {
		t.Fatalf("the status carries body text: %s", stdout)
	}
	for _, text := range []string{"ours", "theirs"} {
		if strings.Contains(stdout, `"text":"`+text) {
			t.Fatalf("the status quotes the %s body: %s", text, stdout)
		}
	}

	code, stdout, stderr = run(t, state.second, "sync", "--status")
	if code != 0 {
		t.Fatalf("sync --status = (%d, %q, %q)", code, stdout, stderr)
	}
	if want := "Relic conflict:\t" + state.relicID + "\tbody\t"; !strings.Contains(stdout, want) {
		t.Fatalf("sync --status printed %q, want a line starting %q", stdout, want)
	}
}
