package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testenv"
	"github.com/dgoings/workbook/internal/testrepo"
)

// A relic change whose publication to the old place was under way when the
// move marked it is refused as a relic: the refusal lists it under relics,
// not tasks, and its message names the relic first, as every relic error
// does.
func TestRelicPublishedAroundTheMarkIsReportedAsARelic(t *testing.T) {
	t.Parallel()
	project := codeProject(t, testrepo.FormatSHA1)
	teammate := cloneCode(t, project)
	if code, stdout, stderr := run(t, teammate, "setup"); code != 0 {
		t.Fatalf("teammate setup = (%d, %q, %q)", code, stdout, stderr)
	}
	relicID := mustCreateRelic(t, teammate, "Moving plan", "--type", "plan").Relic.ID
	held, release := holdPushesTo(t, project.code, "refs/workbook/relics/*")

	type outcome struct {
		code           int
		stdout, stderr string
	}
	finished := make(chan outcome, 1)
	go func() {
		code, stdout, stderr := run(t, teammate, "relic", "update", relicID, "--title", "Edited as the tasks moved", "--json")
		finished <- outcome{code, stdout, stderr}
	}()
	select {
	case <-held:
	case result := <-finished:
		release()
		t.Fatalf("the teammate's relic update finished before its push was held: %+v", result)
	}
	project.moveTasks(t)
	release()
	var result outcome
	select {
	case result = <-finished:
	case <-time.After(60 * time.Second):
		t.Fatal("the teammate's relic update never finished")
	}

	if result.code != 3 {
		t.Fatalf("teammate relic update = (%d, %q, %q), want 3", result.code, result.stdout, result.stderr)
	}
	refusal := requireTasksRemoteRefusal(t, result.stderr, core.CategoryNotInitialized, core.TasksRemotePublishedAfterMove)
	if len(refusal.Tasks) != 0 || len(refusal.Relics) != 1 || refusal.Relics[0] != relicID {
		t.Fatalf("refusal = %+v, want the one relic and no task", refusal)
	}
	message := errorDocumentOf(t, result.stderr).Error.Message
	if !strings.HasPrefix(message, "relic "+relicID+": ") || !strings.Contains(message, "kept in this checkout") {
		t.Fatalf("refusal message %q, want it to start with the relic and say the change is kept", message)
	}
}

// A relic push the remote accepted, after which listing the remote fails,
// warns that it could not be confirmed, and the warning names the relic
// first.
func TestRelicPublicationThatCannotBeCheckedAfterwardsNamesTheRelic(t *testing.T) {
	t.Parallel()
	_, clone := cliSyncRepositories(t)
	if code, stdout, stderr := run(t, clone, "setup"); code != 0 {
		t.Fatalf("setup = (%d, %q, %q)", code, stdout, stderr)
	}
	relicID := mustCreateRelic(t, clone, "Accepted plan", "--type", "plan").Relic.ID
	bare := strings.TrimSpace(cliGitOutput(t, clone, "remote", "get-url", "origin"))
	gone := bare + ".gone"
	hook := "#!/bin/sh\nmoved=no\nwhile read old new ref; do\n" +
		"  case \"$ref\" in refs/workbook/relics/*) moved=yes ;; esac\ndone\n" +
		"if [ \"$moved\" = yes ]; then mv '" + filepath.ToSlash(bare) + "' '" + filepath.ToSlash(gone) + "'; fi\nexit 0\n"
	testenv.WriteExecutable(t, filepath.Join(bare, "hooks", "post-receive"), []byte(hook))

	envelope := runRelicJSON(t, clone, 0, "update", relicID, "--title", "Accepted, then out of reach")
	if err := os.Rename(gone, bare); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, warning := range envelope.Warnings {
		if warning.Code != core.WarningPublicationUnconfirmed {
			continue
		}
		found = true
		if !strings.HasPrefix(warning.Message, "relic "+relicID+": origin accepted it") {
			t.Fatalf("warning %q, want it to start with the relic", warning.Message)
		}
	}
	if !found {
		t.Fatalf("warnings = %+v, want the unconfirmed publication", envelope.Warnings)
	}
}
