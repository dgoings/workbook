package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

func decodeDisplayResult(t *testing.T, output, command string) configDisplayResult {
	t.Helper()
	var result configDisplayResult
	if err := json.Unmarshal(assertJSONResult(t, output, command).Data, &result); err != nil {
		t.Fatalf("decode %s: %v; output = %q", command, err, output)
	}
	return result
}

func decodeConfigShow(t *testing.T, output string) configShowResult {
	t.Helper()
	var result configShowResult
	if err := json.Unmarshal(assertJSONResult(t, output, "config show").Data, &result); err != nil {
		t.Fatalf("decode config show: %v; output = %q", err, output)
	}
	return result
}

func settingView(t *testing.T, result configShowResult, setting string) displaySettingView {
	t.Helper()
	for _, view := range result.Display.Settings {
		if view.Setting == setting {
			return view
		}
	}
	t.Fatalf("config show reports no %q setting: %#v", setting, result.Display.Settings)
	return displaySettingView{}
}

// The whole round trip of a ledger-backed setting through the command line:
// recorded, reported with its source, undone by the exact command the result
// printed, and readable in the ledger's own log.
func TestConfigSetRecordsADisplaySettingAndSaysHowToUndoIt(t *testing.T) {
	repository := initializedRepository(t)

	code, stdout, stderr := run(t, repository, "config", "set", "project-name", "Atlas", "--json")
	if code != 0 {
		t.Fatalf("config set code = %d; stderr = %q", code, stderr)
	}
	result := decodeDisplayResult(t, stdout, "config set")
	if result.Change.Setting != core.DisplayProjectName || result.Change.Value != "Atlas" {
		t.Fatalf("change = %#v", result.Change)
	}
	if result.Change.Operation != "set" || result.Change.From != "" {
		t.Fatalf("change = %#v, want a set with no previous value", result.Change)
	}
	// Nothing was configured before, so the exact inverse is the clearing verb
	// rather than a set of an empty value.
	if result.Inverse.Command != "workbook config unset project-name" || !result.Inverse.Exact {
		t.Fatalf("inverse = %#v, want an exact unset", result.Inverse)
	}
	if result.Display.Head == "" || !result.Display.Seeded {
		t.Fatalf("display view = %#v, want the ledger tip this write produced", result.Display)
	}

	shown := decodeConfigShow(t, mustRun(t, repository, "config", "show", "--json"))
	name := settingView(t, shown, core.DisplayProjectName)
	if name.Value != "Atlas" || name.Source != "configured" {
		t.Fatalf("config show project-name = %#v, want the configured value", name)
	}
	color := settingView(t, shown, core.DisplayPrimaryColor)
	if color.Value != "" || color.Source != "default" {
		t.Fatalf("config show primary-color = %#v, want an unconfigured default", color)
	}

	// The change is in the ledger's log, summarized in words and carrying the
	// same inverse the mutation printed.
	logged := mustRun(t, repository, "status", "log", "--json")
	var logResult statusLogResult
	if err := json.Unmarshal(assertJSONResult(t, logged, "status log").Data, &logResult); err != nil {
		t.Fatalf("decode status log: %v", err)
	}
	if len(logResult.Entries) == 0 {
		t.Fatal("status log is empty after a display change")
	}
	entry := logResult.Entries[len(logResult.Entries)-1]
	if entry.Operation != core.ConfigDisplaySet {
		t.Fatalf("newest log entry = %#v, want the display.set", entry)
	}
	if !strings.Contains(entry.Summary, "project-name") || !strings.Contains(entry.Summary, "Atlas") {
		t.Fatalf("log summary = %q, want it to name the setting and the value", entry.Summary)
	}
	if entry.Inverse == nil || entry.Inverse.Command != "workbook config unset project-name" {
		t.Fatalf("log inverse = %#v, want the clearing command", entry.Inverse)
	}

	// Setting it again reports what it replaced, and its inverse restores that.
	replaced := decodeDisplayResult(t,
		mustRun(t, repository, "config", "set", "project-name", "Borealis", "--json"), "config set")
	if replaced.Change.From != "Atlas" {
		t.Fatalf("change = %#v, want the previous value reported", replaced.Change)
	}
	if replaced.Inverse.Command != "workbook config set project-name Atlas" || !replaced.Inverse.Exact {
		t.Fatalf("inverse = %#v, want an exact restore of the old name", replaced.Inverse)
	}

	// And clearing it inverts back to the value it cleared.
	cleared := decodeDisplayResult(t,
		mustRun(t, repository, "config", "unset", "project-name", "--json"), "config unset")
	if cleared.Change.Operation != "unset" || cleared.Change.From != "Borealis" {
		t.Fatalf("change = %#v", cleared.Change)
	}
	if cleared.Inverse.Command != "workbook config set project-name Borealis" || !cleared.Inverse.Exact {
		t.Fatalf("inverse = %#v", cleared.Inverse)
	}
	if got := settingView(t, decodeConfigShow(t, mustRun(t, repository, "config", "show", "--json")),
		core.DisplayProjectName); got.Source != "default" {
		t.Fatalf("config show after clearing = %#v, want the default again", got)
	}

	// The strongest statement available that the section really folds: validate
	// replays the whole configuration ledger from its root and compares every
	// stored checkpoint against the one it recomputes, byte for byte.
	if code, _, stderr := run(t, repository, "validate", "--full", "--json"); code != 0 {
		t.Fatalf("validate --full code = %d, want 0; stderr = %q", code, stderr)
	}
}

// A color typed in either case is one stored value, because the checkpoint the
// ledger compares is compared by bytes.
func TestConfigSetFoldsAColorToItsCanonicalForm(t *testing.T) {
	repository := initializedRepository(t)
	result := decodeDisplayResult(t,
		mustRun(t, repository, "config", "set", "primary-color", "#1A7F4B", "--json"), "config set")
	if result.Change.Value != "#1a7f4b" {
		t.Fatalf("stored color = %q, want the lowercase form", result.Change.Value)
	}
	// A name is trimmed on the same terms.
	named := decodeDisplayResult(t,
		mustRun(t, repository, "config", "set", "project-name", "  Atlas  ", "--json"), "config set")
	if named.Change.Value != "Atlas" {
		t.Fatalf("stored name = %q, want it trimmed", named.Change.Value)
	}
}

// Every refusal a person can provoke at this boundary is a validation failure
// quoting the rule, not a report that the repository is damaged.
func TestConfigSetRefusesWhatTheDisplayRulesForbid(t *testing.T) {
	repository := initializedRepository(t)
	for _, testCase := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"unknown setting", []string{"config", "set", "primary-colour", "#1a7f4b"}, 5, "primary-colour"},
		{"three-digit color", []string{"config", "set", "primary-color", "#abc"}, 5, "hexadecimal"},
		{"named color", []string{"config", "set", "text-color", "rebeccapurple"}, 5, "hexadecimal"},
		{"blank name", []string{"config", "set", "project-name", "   "}, 5, "blank"},
		{"oversized name", []string{"config", "set", "project-name", strings.Repeat("n", core.MaxProjectNameBytes+1)}, 5, "100"},
		{"unknown setting on unset", []string{"config", "unset", "board-color"}, 5, "board-color"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := run(t, repository, append(testCase.args, "--json")...)
			if code != testCase.code {
				t.Fatalf("code = %d, want %d; stdout = %q stderr = %q", code, testCase.code, stdout, stderr)
			}
			assertJSONError(t, stderr, core.CategoryValidation, "")
			if !strings.Contains(stderr, testCase.message) {
				t.Fatalf("error = %q, want it to mention %q", stderr, testCase.message)
			}
		})
	}
	// The tracked-file setting still behaves as it did, which is the other half
	// of one command family serving two kinds of setting.
	if code, _, stderr := run(t, repository, "config", "set", "auto-sync", "false", "--json"); code != 0 {
		t.Fatalf("config set auto-sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, repository, "config", "set", "auto-sync", "maybe", "--json"); code != 5 {
		t.Fatalf("config set auto-sync maybe code = %d, want 5; stderr = %q", code, stderr)
	}
}

// --no-sync means the same thing here it means everywhere: record it locally
// and do not talk to origin.
func TestConfigSetHonorsNoSync(t *testing.T) {
	writer, _ := cliSyncRepositories(t)
	if code, stdout, stderr := run(t, writer, "config", "set", "project-name", "Atlas", "--no-sync", "--json"); code != 0 {
		t.Fatalf("config set --no-sync code = %d; stdout = %q stderr = %q", code, stdout, stderr)
	}
	if got := cliGitOutput(t, writer, "ls-remote", "origin", "refs/workbook/config"); strings.TrimSpace(got) != "" {
		t.Fatalf("origin holds the configuration ledger after --no-sync: %q", got)
	}
	// Without the flag the same command publishes.
	if code, _, stderr := run(t, writer, "config", "set", "text-color", "#101820", "--json"); code != 0 {
		t.Fatalf("config set code = %d; stderr = %q", code, stderr)
	}
	if got := cliGitOutput(t, writer, "ls-remote", "origin", "refs/workbook/config"); strings.TrimSpace(got) == "" {
		t.Fatalf("origin does not hold the configuration ledger after a synchronized write")
	}
}

// The text surface reports what the JSON one does, in the shape the rest of the
// CLI uses: a column-zero heading and tab-indented fields.
func TestConfigSetRendersTheChangeAsText(t *testing.T) {
	repository := initializedRepository(t)
	stdout := mustRun(t, repository, "config", "set", "project-name", "Atlas")
	for _, wanted := range []string{"Display:\tset\tproject-name", "\tvalue:\tAtlas", "\tinverse:\tworkbook config unset project-name"} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("config set output = %q, want it to contain %q", stdout, wanted)
		}
	}
	shown := mustRun(t, repository, "config", "show")
	if !strings.Contains(shown, "project-name:\tAtlas\t(configured)") {
		t.Fatalf("config show output = %q, want the configured name and its source", shown)
	}
	if !strings.Contains(shown, "primary-color:\t(default)") {
		t.Fatalf("config show output = %q, want an unconfigured color to read as a default", shown)
	}

	// A project nobody has configured says what its board falls back to, so
	// "default" is a value somebody can read rather than a blank column.
	fresh := mustRun(t, initializedRepository(t), "config", "show")
	if !strings.Contains(fresh, "project-name:\t"+core.DefaultProjectName+"\t(default)") {
		t.Fatalf("config show output = %q, want it to name the default a board falls back to", fresh)
	}
}

// The mixed-version contract, against a real generation-one process and a real
// display setting written by this build.
//
// Generation one is every clone running v0.5.0, and this is the whole question
// the bump raises: what happens to a teammate who has not upgraded when
// somebody names the project. Everything asserted here is what the marker was
// raised to buy — the older clone keeps reading the project, keeps
// synchronizing, and is told to upgrade rather than told its repository is
// broken when it tries to change the configuration.
func TestAGenerationOneReaderParksOnADisplayConfiguredProject(t *testing.T) {
	binary := buildPatchedGenerationBinary(t, 1)
	writer, older := cliSyncRepositories(t)

	task := cliCreateTask(t, writer, "Ordinary task")
	if code, _, stderr := run(t, writer, "sync"); code != 0 {
		t.Fatalf("writer sync code = %d; stderr = %q", code, stderr)
	}
	// The older clone starts from a project it understands completely.
	if code, _, stderr := runBinary(t, binary, older, "sync"); code != 0 {
		t.Fatalf("older clone initial sync code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, writer, "config", "set", "project-name", "Atlas", "--json"); code != 0 {
		t.Fatalf("config set code = %d; stderr = %q", code, stderr)
	}

	// The marker really is on the documents the older build will read.
	head := cliGitOutput(t, writer, "rev-parse", "refs/workbook/config")
	for _, name := range []string{"operation.json", "state.json"} {
		document := cliGitOutput(t, writer, "show", head+":"+name)
		if !strings.Contains(document, `"minReader":2`) {
			t.Fatalf("%s carries no generation-two marker: %s", name, document)
		}
	}

	// Synchronization advances what it can. The configuration ledger is a
	// fast-forward for a clone that has authored none of its own, so nothing is
	// refused here at all — which is the property that keeps an un-upgraded
	// teammate receiving everybody else's work.
	if code, stdout, stderr := runBinary(t, binary, older, "sync", "--json"); code != 0 {
		t.Fatalf("older clone sync code = %d, want 0; stdout = %q stderr = %q", code, stdout, stderr)
	}
	if got := cliGitOutput(t, older, "rev-parse", "refs/workbook/config"); got != head {
		t.Fatalf("older clone configuration ref = %q, want origin's tip %q", got, head)
	}

	// Reads still work: the statuses come out of the checkpoint it could not
	// decode strictly but could decode.
	code, stdout, stderr := runBinary(t, binary, older, "status", "list", "--json")
	if code != 0 {
		t.Fatalf("older clone status list code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "backlog") {
		t.Fatalf("older clone status list = %q, want it to still name the project's statuses", stdout)
	}
	if code, _, stderr := runBinary(t, binary, older, "list", "--json"); code != 0 {
		t.Fatalf("older clone list code = %d, want 0; stderr = %q", code, stderr)
	}

	// Changing the configuration is refused with the upgrade signal, not with a
	// corruption report.
	code, _, stderr = runBinary(t, binary, older, "status", "add", "triage", "--no-sync", "--json")
	if code != 9 {
		t.Fatalf("older clone status add code = %d, want 9 (newer-writer); stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNewerWriter, "")
	for _, wanted := range []string{"newer workbook", "upgrade workbook"} {
		if !strings.Contains(stderr, wanted) {
			t.Fatalf("refusal = %q, want it to contain %q", stderr, wanted)
		}
	}
	for _, forbidden := range []string{"corrupt", "damaged", "invalid history"} {
		if strings.Contains(strings.ToLower(stderr), forbidden) {
			t.Fatalf("refusal = %q, want no claim that the repository is %s", stderr, forbidden)
		}
	}

	// And the scope is the configuration alone. Tasks are a different ref and a
	// different generation, so the older clone still does its own work.
	if code, _, stderr := runBinary(t, binary, older, "update", task.ID, "--title", "Still editable", "--json"); code != 0 {
		t.Fatalf("older clone update code = %d, want 0; stderr = %q", code, stderr)
	}
}

func mustRun(t *testing.T, repository string, args ...string) string {
	t.Helper()
	code, stdout, stderr := run(t, repository, args...)
	if code != 0 {
		t.Fatalf("workbook %s code = %d; stderr = %q", strings.Join(args, " "), code, stderr)
	}
	return stdout
}
