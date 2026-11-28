package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// relicReadOutputs records what every relic read command prints for the
// relics in ids.
func relicReadOutputs(t *testing.T, repository string, ids []string) []string {
	t.Helper()
	commands := [][]string{{"relic", "list", "--all", "--json"}}
	for _, id := range ids {
		commands = append(commands,
			[]string{"relic", "show", id, "--body", "--removed", "--json"},
			[]string{"relic", "export", id, "--json"})
	}
	outputs := make([]string, 0, len(commands))
	for _, command := range commands {
		code, stdout, stderr := run(t, repository, command...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s = (%d, %q, %q)", strings.Join(command, " "), code, stdout, stderr)
		}
		outputs = append(outputs, stdout)
	}
	return outputs
}

func relicCachePath(t *testing.T, repository string) string {
	t.Helper()
	commonDir := gitOutput(t, repository, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(repository, commonDir)
	}
	return filepath.Join(commonDir, "workbook", "cache.sqlite")
}

func assertSameOutputs(t *testing.T, label string, got, want []string) {
	t.Helper()
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s: output %d =\n%s\nwant\n%s", label, index, got[index], want[index])
		}
	}
}

func TestRelicReadsAreTheSameAfterTheIndexIsDeleted(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			first := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, relicTestDocument(t)))
			second := mustCreateRelic(t, repository, "Second", "--type", "agenda")
			mustRelic(t, repository, nil, "update", second.Relic.ID, "--title", "Second, revised")
			deleted := mustCreateRelic(t, repository, "Deleted", "--type", "agenda")
			mustRelic(t, repository, nil, "delete", deleted.Relic.ID)
			ids := []string{first.Relic.ID, second.Relic.ID, deleted.Relic.ID}

			want := relicReadOutputs(t, repository, ids)
			cache := relicCachePath(t, repository)
			if err := os.Remove(cache); err != nil {
				t.Fatalf("remove the cache: %v", err)
			}
			assertSameOutputs(t, "after the cache was deleted", relicReadOutputs(t, repository, ids), want)

			code, stdout, stderr := run(t, repository, "rebuild", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("rebuild = (%d, %q, %q)", code, stdout, stderr)
			}
			var rebuilt rebuildResult
			if err := json.Unmarshal(assertJSONResult(t, stdout, "rebuild").Data, &rebuilt); err != nil {
				t.Fatal(err)
			}
			if rebuilt.RelicCount != len(ids) {
				t.Fatalf("rebuild relicCount = %d, want %d", rebuilt.RelicCount, len(ids))
			}
			assertSameOutputs(t, "after a rebuild", relicReadOutputs(t, repository, ids), want)

			info, err := os.Stat(cache)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(cache, info.Size()/2); err != nil {
				t.Fatal(err)
			}
			assertSameOutputs(t, "after the cache was truncated", relicReadOutputs(t, repository, ids), want)
		})
	}
}

func TestRelicShowSeesAnUpdateWithoutARebuild(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Before", "--type", "paper")
	var shown core.RelicDocument
	mustRelic(t, repository, &shown, "show", created.Relic.ID)

	var updated core.RelicMutationResult
	mustRelic(t, repository, &updated, "update", created.Relic.ID, "--title", "After")
	mustRelic(t, repository, &shown, "show", created.Relic.ID)
	if shown.Title != "After" || shown.Head != updated.Relic.Head || shown.Head == created.Relic.Head {
		t.Fatalf("show = %q at %s, want \"After\" at %s", shown.Title, shown.Head, updated.Relic.Head)
	}
}

func TestRebuildReportsRelics(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	if code, _, stderr := run(t, repository, "create", "A task", "--no-sync"); code != 0 {
		t.Fatalf("create = %d; stderr = %q", code, stderr)
	}
	mustCreateRelic(t, repository, "One", "--type", "paper")
	mustCreateRelic(t, repository, "Two", "--type", "paper")

	code, stdout, stderr := run(t, repository, "rebuild", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("rebuild --json = (%d, %q, %q)", code, stdout, stderr)
	}
	var data map[string]any
	if err := json.Unmarshal(assertJSONResult(t, stdout, "rebuild").Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["taskCount"] != float64(1) || data["relicCount"] != float64(2) {
		t.Fatalf("rebuild --json data = %v, want taskCount 1 and relicCount 2", data)
	}
	code, stdout, stderr = run(t, repository, "rebuild")
	if code != 0 || stderr != "" {
		t.Fatalf("rebuild = (%d, %q, %q)", code, stdout, stderr)
	}
	if want := fmt.Sprintf("Rebuilt 1 task(s) and 2 relic(s) at %s.\n", data["cachePath"]); stdout != want {
		t.Fatalf("rebuild printed %q, want %q", stdout, want)
	}
}

// Two worktrees of one repository share one index, and separate processes in
// each read and change relics at once.
func TestRelicCommandsInTwoWorktreesAndProcessesShareOneIndex(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	repository := initializedRepository(t)
	second := filepath.Join(t.TempDir(), "second")
	gitOutput(t, repository, "add", ".workbook/config.json")
	gitOutput(t, repository, "commit", "--quiet", "-m", "Initialize Workbook")
	gitOutput(t, repository, "worktree", "add", "--quiet", "-b", "second", second)
	worktrees := []string{repository, second}
	var ids []string
	for index := range worktrees {
		ids = append(ids, mustCreateRelic(t, repository, fmt.Sprintf("Relic %d", index), "--type", "paper").Relic.ID)
	}

	const jobs = 80
	work := make([]func() []raceReply, jobs)
	for index := range work {
		worktree := index % 2
		switch index % 4 {
		case 0, 1:
			title := fmt.Sprintf("Title %03d", index)
			work[index] = one(func() raceReply {
				return processReply(binary, worktrees[worktree], false, "relic", "update", ids[worktree], "--title", title, "--json")
			})
		case 2:
			work[index] = one(func() raceReply {
				return processReply(binary, worktrees[worktree], true, "relic", "list", "--json")
			})
		case 3:
			work[index] = one(func() raceReply {
				return processReply(binary, worktrees[worktree], true, "relic", "show", ids[1-worktree], "--json")
			})
		}
	}
	tallyRace(t, race(t, work))

	listings := make([]string, len(worktrees))
	for index, worktree := range worktrees {
		code, stdout, stderr := run(t, worktree, "relic", "list", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("relic list in %s = (%d, %q, %q)", worktree, code, stdout, stderr)
		}
		listings[index] = stdout
	}
	if listings[0] != listings[1] {
		t.Fatalf("the worktrees list\n%s\nand\n%s", listings[0], listings[1])
	}
	var summaries []core.RelicSummary
	if err := json.Unmarshal(assertJSONResult(t, listings[0], "relic list").Data, &summaries); err != nil {
		t.Fatal(err)
	}
	for _, summary := range summaries {
		head := gitOutput(t, repository, "rev-parse", "refs/workbook/relics/"+summary.ID)
		var pack struct {
			Operations []struct {
				Title string `json:"title"`
			} `json:"operations"`
		}
		if err := json.Unmarshal([]byte(gitOutput(t, repository, "cat-file", "blob", head+":operation.json")), &pack); err != nil {
			t.Fatal(err)
		}
		if summary.Head != head || len(pack.Operations) != 1 || summary.Title != pack.Operations[0].Title {
			t.Fatalf("relic %s lists %q at %s, want the last update recorded in Git, %+v at %s",
				summary.ID, summary.Title, summary.Head, pack.Operations, head)
		}
	}
}

// A damaged task tip stops the rebuild a deleted cache needs. Relic reads
// still answer from Git, exactly as with a healthy index and with nothing on
// stderr, while a task command still fails as it does without relics.
func TestRelicReadsAnswerFromGitWhenATaskTipIsDamaged(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)
			first := mustCreateRelic(t, repository, "--from-file", writeRelicDocumentFile(t, relicTestDocument(t)))
			second := mustCreateRelic(t, repository, "Second", "--type", "agenda")
			mustRelic(t, repository, nil, "update", second.Relic.ID, "--title", "Second, revised")
			ids := []string{first.Relic.ID, second.Relic.ID}
			commands := [][]string{{"relic", "list", "--json"}, {"relic", "show", first.Relic.ID, "--json"}}
			healthy := func() []string {
				outputs := relicReadOutputs(t, repository, ids)
				for _, command := range commands {
					code, stdout, stderr := run(t, repository, command...)
					if code != 0 || stderr != "" {
						t.Fatalf("%s = (%d, %q, %q)", strings.Join(command, " "), code, stdout, stderr)
					}
					outputs = append(outputs, stdout)
				}
				return outputs
			}
			want := healthy()

			if code, _, stderr := run(t, repository, "create", "A task", "--no-sync"); code != 0 {
				t.Fatalf("create = %d; stderr = %q", code, stderr)
			}
			taskRef := gitOutput(t, repository, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/")
			blob := cliGitOutputInput(t, repository, "not a commit\n", "hash-object", "-w", "--stdin")
			cliGit(t, repository, "update-ref", taskRef, blob)
			cache := relicCachePath(t, repository)
			if err := os.Remove(cache); err != nil {
				t.Fatalf("remove the cache: %v", err)
			}
			taskCode, taskStdout, taskStderr := run(t, repository, "list", "--json")
			if taskCode == 0 {
				t.Fatalf("list succeeded with a damaged task tip: %q", taskStdout)
			}
			if err := os.Remove(cache); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove the cache: %v", err)
			}

			assertSameOutputs(t, "with a damaged task tip", healthy(), want)

			if err := os.Remove(cache); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove the cache: %v", err)
			}
			code, stdout, stderr := run(t, repository, "list", "--json")
			if code != taskCode || stdout != taskStdout || stderr != taskStderr {
				t.Fatalf("list after the relic reads = (%d, %q, %q), want (%d, %q, %q)",
					code, stdout, stderr, taskCode, taskStdout, taskStderr)
			}
		})
	}
}

// A cache SQLite cannot open, here a directory where the cache file goes,
// leaves relic reads answering from Git with nothing on stderr.
func TestRelicReadsAnswerFromGitWhenTheCacheCannotBeOpened(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	created := mustCreateRelic(t, repository, "Readable", "--type", "paper")
	ids := []string{created.Relic.ID}
	want := relicReadOutputs(t, repository, ids)
	cache := relicCachePath(t, repository)
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	assertSameOutputs(t, "with no usable cache", relicReadOutputs(t, repository, ids), want)
}

// A relic stored with times in a zone of its own, as a tool other than
// Workbook may write it, reads as valid. JSON output, the export and the
// index keep the offset; the text of relic show gives the time in UTC. An
// edit through Workbook records its own time in UTC and leaves the creation
// time as stored.
func TestARelicStoredWithAnOffsetKeepsIt(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	fixture := openRelicFixture(t, repository)
	zone := time.FixedZone("", -(3*60+30)*60)
	stored := relicTestTime.In(zone)
	relicID := relicTestRelicID(1)
	pack := core.NewRelicOperationPack(fixture.config.ProjectID, relicID, relicTestID(900001), relicTestActor, 1, stored,
		[]core.RelicOperation{{
			ID:    relicTestID(800001),
			Type:  core.RelicOperationCreate,
			Relic: &core.RelicCreateData{Title: "Kept in a zone", Type: "spec", CreatedAt: stored},
		}})
	fixture.write(t, nil, pack, nil)
	const offset = `"2026-11-23T06:30:00-03:30"`

	outputs := relicReadOutputs(t, repository, []string{relicID})
	for index, output := range outputs {
		if strings.Count(output, `"createdAt":`+offset) != 1 || strings.Count(output, `"updatedAt":`+offset) != 1 {
			t.Fatalf("output %d does not keep the stored offset: %s", index, output)
		}
	}
	if err := os.Remove(relicCachePath(t, repository)); err != nil {
		t.Fatalf("remove the cache: %v", err)
	}
	assertSameOutputs(t, "after the cache was deleted", relicReadOutputs(t, repository, []string{relicID}), outputs)

	code, stdout, stderr := run(t, repository, "relic", "show", relicID)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Updated:\t2026-11-23T10:00:00Z\n") {
		t.Fatalf("relic show = (%d, %q, %q), want the update time in UTC", code, stdout, stderr)
	}
	if code, stdout, stderr := run(t, repository, "validate"); code != 0 {
		t.Fatalf("validate = (%d, %q, %q)", code, stdout, stderr)
	}

	mustRelic(t, repository, nil, "update", relicID, "--title", "Edited here")
	code, stdout, stderr = run(t, repository, "relic", "show", relicID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("relic show --json = (%d, %q, %q)", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"createdAt":`+offset) || strings.Contains(stdout, `"updatedAt":`+offset) ||
		!strings.Contains(stdout, `Z","properties"`) {
		t.Fatalf("after an edit, relic show --json = %s; want the creation time as stored and the update time in UTC", stdout)
	}
	if code, stdout, stderr := run(t, repository, "validate"); code != 0 {
		t.Fatalf("validate after an edit = (%d, %q, %q)", code, stdout, stderr)
	}
}
