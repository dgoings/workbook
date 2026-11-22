package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testrepo"
)

// unconfiguredSessionExpectation is what the scripted session below did when
// it was run with a binary built from origin/main before the tasks-remote
// setting existed. Set WORKBOOK_RECORD_UNCONFIGURED_SESSION to the path of
// such a binary to record it again.
const unconfiguredSessionExpectation = "testdata/unconfigured-session.txt"

// unconfiguredSessionCommands is the scripted session.
var unconfiguredSessionCommands = [][]string{
	{"setup", "--json"},
	{"create", "Unconfigured", "--json"},
	{"next", "--claim", "--json"},
	{"fetch", "--json"},
	{"push", "--json"},
	{"sync", "--json"},
	{"config", "show", "--json"},
	{"list", "--json"},
}

// The Git commands this branch runs differently in a project that never
// chose a tasks remote, which docs/reference.md lists. Every command's first
// listing of the identity ref also names origin's tracking identity ref: the
// checkout already holds it, so a marker published to origin is noticed
// without asking origin anything. A command that publishes task refs also
// reads the histories it publishes once, to refuse one that records an
// operation twice, and lists origin's identity ref once after origin accepts
// them, to report an edit that landed just as origin was marked as moved.
var (
	identityListingBefore = "git for-each-ref --format=%(refname)%00%(objectname)%00%(symref) refs/workbook/project"
	identityListingNow    = identityListingBefore + " refs/workbook/remotes/origin/project"
	publicationChecks     = []string{
		"git cat-file --batch-check",
		"git rev-list --reverse --topo-order --parents --stdin",
		"git cat-file --batch",
		"git ls-remote --refs origin refs/workbook/project",
	}
	publishingCommands = map[string]bool{"create Unconfigured --json": true, "next --claim --json": true}
)

var (
	objectIDPattern = regexp.MustCompile(`\b[0-9a-f]{64}\b|\b[0-9a-f]{40}\b`)
	ulidPattern     = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{26}`)
	// A task ID, or the short form a commit message carries, under the key
	// this project was given, which depends on the test's folder name.
	taskIDPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-(<ulid>|[0-9A-HJKMNP-TV-Z]{8}\b)`)
)

// runTracedSession runs the scripted session with binary in clone and returns,
// per command, the sorted top-level Git commands it ran, normalized so two
// runs compare: object IDs, ULIDs and the test's folders are replaced by
// placeholders. Top-level means run by Workbook rather than by Git itself,
// which trace2 tells apart by a session ID without a parent.
func runTracedSession(t *testing.T, binary, clone string, replacements map[string]string) string {
	t.Helper()
	var report strings.Builder
	for _, args := range unconfiguredSessionCommands {
		events := filepath.Join(t.TempDir(), "trace2.json")
		command := exec.Command(binary, args...)
		command.Dir = clone
		command.Env = append(os.Environ(), "GIT_TRACE2_EVENT="+events)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output)
		}
		var lines []string
		file, err := os.Open(events)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 1<<20), 1<<24)
		for scanner.Scan() {
			var event struct {
				Event string   `json:"event"`
				SID   string   `json:"sid"`
				Argv  []string `json:"argv"`
			}
			if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Event != "start" || strings.Contains(event.SID, "/") {
				continue
			}
			argv := event.Argv
			// Workbook names its folder with -C; the folder is the same for
			// every command, so it is dropped rather than compared.
			if len(argv) > 2 && argv[1] == "-C" {
				argv = append([]string{argv[0]}, argv[3:]...)
			}
			argv[0] = "git"
			lines = append(lines, normalizeSessionText(strings.Join(argv, " "), replacements))
		}
		file.Close()
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(lines)
		report.WriteString("== workbook " + strings.Join(args, " ") + "\n")
		for _, line := range lines {
			report.WriteString(line + "\n")
		}
	}
	return report.String()
}

func normalizeSessionText(text string, replacements map[string]string) string {
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	// Longest first, so a folder inside another is replaced as itself.
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, key := range keys {
		text = strings.ReplaceAll(text, key, replacements[key])
	}
	text = objectIDPattern.ReplaceAllString(text, "<oid>")
	text = ulidPattern.ReplaceAllString(text, "<ulid>")
	return taskIDPattern.ReplaceAllString(text, "<task>")
}

// sessionRefs lists every ref a repository holds, by name, normalized.
func sessionRefs(t *testing.T, repository string, replacements map[string]string) string {
	t.Helper()
	names := strings.Fields(cliGitOutput(t, repository, "for-each-ref", "--format=%(refname)"))
	for index, name := range names {
		names[index] = normalizeSessionText(name, replacements)
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}

// A project that never chose a tasks remote runs, for a scripted session in a
// clone that happens to have a remote named tasks, the Git commands a build
// from before the setting ran, apart from the reads listed above, and ends
// with the same refs in the clone, on origin and on that remote, which is
// never contacted.
func TestTasksRemoteUnconfiguredSessionMatchesTheReleaseBeforeIt(t *testing.T) {
	t.Parallel()
	binary := buildWorkbookBinary(t)
	recording := os.Getenv("WORKBOOK_RECORD_UNCONFIGURED_SESSION")
	if recording != "" {
		binary = recording
	}
	project := codeProject(t, testrepo.FormatSHA1)
	decoy := newBareRepository(t, testrepo.FormatSHA1)
	clone := cloneCode(t, project)
	cliGit(t, clone, "remote", "add", "tasks", decoy)
	if code, _, stderr := run(t, project.seed, "update", project.ids[0], "--status", "ready"); code != 0 {
		t.Fatalf("make ready = %d; stderr = %q", code, stderr)
	}
	replacements := map[string]string{clone: "<clone>", project.code: "<origin>", decoy: "<decoy>"}

	got := runTracedSession(t, binary, clone, replacements)
	got += "== refs in the clone\n" + sessionRefs(t, clone, replacements) + "\n"
	got += "== refs on origin\n" + sessionRefs(t, project.code, replacements) + "\n"
	got += "== refs on the remote named tasks\n" + sessionRefs(t, decoy, replacements) + "\n"

	if recording != "" {
		if err := os.WriteFile(unconfiguredSessionExpectation, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s with %s", unconfiguredSessionExpectation, recording)
		return
	}
	recorded, err := os.ReadFile(unconfiguredSessionExpectation)
	if err != nil {
		t.Fatal(err)
	}
	want := withThisBranchsChecks(t, string(recorded))
	if got != want {
		t.Fatalf("the unconfigured session differs from the release before the setting:\n%s", sessionDiff(want, got))
	}
}

// withThisBranchsChecks turns the recorded session into what this branch is
// expected to run: the documented differences above, and nothing else.
func withThisBranchsChecks(t *testing.T, report string) string {
	t.Helper()
	var out strings.Builder
	var section []string
	var command string
	flush := func() {
		if command != "" {
			replaced := false
			for index, line := range section {
				if line == identityListingBefore {
					section[index], replaced = identityListingNow, true
					break
				}
			}
			if !replaced {
				t.Fatalf("%s no longer lists the identity ref where this branch changes it", unconfiguredSessionExpectation)
			}
		}
		if publishingCommands[command] {
			section = append(section, publicationChecks...)
		}
		sort.Strings(section)
		for _, line := range section {
			out.WriteString(line + "\n")
		}
		section, command = nil, ""
	}
	sorting := false
	for _, line := range strings.Split(strings.TrimSuffix(report, "\n"), "\n") {
		if strings.HasPrefix(line, "== ") {
			flush()
			sorting = strings.HasPrefix(line, "== workbook ")
			if sorting {
				command = strings.TrimPrefix(line, "== workbook ")
			}
			out.WriteString(line + "\n")
			continue
		}
		if sorting {
			section = append(section, line)
		} else {
			out.WriteString(line + "\n")
		}
	}
	flush()
	return out.String()
}

func sessionDiff(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	var out strings.Builder
	limit := len(wantLines)
	if len(gotLines) > limit {
		limit = len(gotLines)
	}
	for index := 0; index < limit; index++ {
		var w, g string
		if index < len(wantLines) {
			w = wantLines[index]
		}
		if index < len(gotLines) {
			g = gotLines[index]
		}
		if w != g {
			out.WriteString("line " + strconv.Itoa(index+1) + ":\n  want " + w + "\n  got  " + g + "\n")
		}
	}
	return out.String()
}
