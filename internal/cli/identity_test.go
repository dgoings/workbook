package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetupJoinsOriginIdentityRefWithNoCommittedConfigAnywhere is the bootstrap
// this story exists for. Origin publishes its identity ref but no branch in the
// repository ever gained `.workbook/config.json`, so the older probe — read
// origin's default branch — has nothing to find. Setup must still join the
// project rather than mint a second one.
func TestSetupJoinsOriginIdentityRefWithNoCommittedConfigAnywhere(t *testing.T) {
	_, seed, stale := originAdoptedAfterClone(t)

	// The seed adopts Workbook and shares tasks, but deliberately never commits
	// its configuration to any branch.
	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	seeded := []string{
		cliCreateTask(t, seed, "Shared with nothing committed").ID,
		cliCreateTask(t, seed, "Also shared with nothing committed").ID,
	}
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push code = %d, want 0; stderr = %q", code, stderr)
	}
	if gitOutput(t, seed, "status", "--porcelain", "--", ".workbook") == "" {
		t.Fatal("the fixture committed the tracked configuration; this test needs it uncommitted")
	}

	code, stdout, stderr := run(t, stale, "setup")
	if code != 0 {
		t.Fatalf("stale setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got, want := projectID(t, stdout), projectIDOf(t, seed); got != want {
		t.Fatalf("stale checkout adopted project %q, want origin's %q", got, want)
	}
	if got, want := projectIdentitySource(t, stdout), "(adopted from the published project identity)"; got != want {
		t.Fatalf("stale setup identity source = %q, want %q", got, want)
	}
	if code, _, stderr := run(t, stale, "sync"); code != 0 {
		t.Fatalf("stale sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := listedTaskIDs(t, stale); !sameIDs(got, seeded) {
		t.Fatalf("adopted tasks = %v, want %v", got, seeded)
	}
}

// Anyone with push access can create a ref under origin's identity name while
// no identity ref exists there, and Git's directory/file rule then blocks the
// identity ref itself. That is origin's namespace, not something a clone may
// refuse to work with: bootstrap and synchronization must complete, skipping
// what they cannot read, exactly as they already do for the task namespace.
func TestSetupAndSyncTolerateRefsUnderOriginsIdentityName(t *testing.T) {
	bare, seed, stale := originAdoptedAfterClone(t)
	blob := cliGitOutput(t, seed, "rev-parse", "HEAD")
	cliGit(t, seed, "push", "--quiet", "origin", blob+":refs/workbook/project/notes")

	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	// The identity ref cannot be published while the name is blocked, so the
	// project falls back to the committed advisory copy — exactly the pre-v0.5.0
	// arrangement, which must keep working.
	cliGit(t, seed, "add", ".workbook/config.json")
	cliGit(t, seed, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, seed, "push", "--quiet", "origin", "main")
	seeded := []string{cliCreateTask(t, seed, "Shared past a blocked identity name").ID}
	if code, _, stderr := run(t, seed, "push"); code != 0 {
		t.Fatalf("seed push code = %d, want 0; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, seed, "sync"); code != 0 {
		t.Fatalf("seed sync code = %d, want 0; stderr = %q", code, stderr)
	}

	if code, _, stderr := run(t, stale, "setup"); code != 0 {
		t.Fatalf("stale setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, stale, "sync"); code != 0 {
		t.Fatalf("stale sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := listedTaskIDs(t, stale); !sameIDs(got, seeded) {
		t.Fatalf("tasks after bootstrap = %v, want %v", got, seeded)
	}

	// Once the obstruction is gone the identity ref publishes normally, and the
	// tracking mirror is pruned of what it could not read.
	cliGit(t, bare, "update-ref", "-d", "refs/workbook/project/notes")
	if code, _, stderr := run(t, seed, "sync"); code != 0 {
		t.Fatalf("seed sync after cleanup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/workbook/project"); got != "refs/workbook/project" {
		t.Fatalf("origin identity refs = %q, want the published identity ref", got)
	}
	if code, _, stderr := run(t, stale, "sync"); code != 0 {
		t.Fatalf("stale sync after cleanup code = %d, want 0; stderr = %q", code, stderr)
	}
}

// A project can be adopted on a branch nobody has merged, by a team that only
// ever mutates tasks: no explicit sync, no explicit push, nothing committed.
// Origin then holds task refs, and unless the mutation path publishes identity
// too, a fresh clone sitting on the pre-Workbook default branch has nothing to
// join and hits the very wedge this story removes.
func TestMutationOnlyFlowPublishesTheIdentityForLaterClones(t *testing.T) {
	bare, seed, later := originAdoptedAfterClone(t)
	// --no-sync bootstraps locally, so nothing but the mutation itself can be
	// what publishes to origin.
	if code, _, stderr := run(t, seed, "setup", "--no-sync"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/workbook/"); got != "" {
		t.Fatalf("origin holds %q after a local-only bootstrap, want nothing", got)
	}

	// An ordinary mutation, synchronizing the way every mutation does by
	// default: it fetches, records the change, and publishes the one ref it
	// touched.
	code, stdout, stderr := run(t, seed, "create", "Recorded by a mutation and nothing else", "--json")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	task := decodeMutationTask(t, stdout, "create")
	if got := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/workbook/project"); got != "refs/workbook/project" {
		t.Fatalf("origin identity refs = %q, want the mutation to have published it", got)
	}
	if !remoteHasTaskRef(t, seed, task.ID) {
		t.Fatalf("origin does not hold task %s", task.ID)
	}

	code, stdout, stderr = run(t, later, "setup")
	if code != 0 {
		t.Fatalf("later setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got, want := projectID(t, stdout), projectIDOf(t, seed); got != want {
		t.Fatalf("later clone adopted project %q, want %q", got, want)
	}
	if got, want := projectIdentitySource(t, stdout), "(adopted from the published project identity)"; got != want {
		t.Fatalf("later setup identity source = %q, want %q", got, want)
	}
	if got := listedTaskIDs(t, later); !sameIDs(got, []string{task.ID}) {
		t.Fatalf("later clone tasks = %v, want %v", got, []string{task.ID})
	}
}

// Publishing a task ref into another project's repository writes this project's
// history where it does not belong. Every publication path has to refuse that,
// not only the one that synchronizes.
func TestPublicationRefusesAnOriginHoldingAnotherProject(t *testing.T) {
	foreignBare, foreignSeed, _ := originAdoptedAfterClone(t)
	if code, _, stderr := run(t, foreignSeed, "setup"); code != 0 {
		t.Fatalf("foreign setup code = %d, want 0; stderr = %q", code, stderr)
	}
	foreignID := projectIDOf(t, foreignSeed)

	_, seed, _ := originAdoptedAfterClone(t)
	if code, _, stderr := run(t, seed, "setup"); code != 0 {
		t.Fatalf("seed setup code = %d, want 0; stderr = %q", code, stderr)
	}
	task := cliCreateTask(t, seed, "Must never reach the wrong repository")
	cliGit(t, seed, "remote", "set-url", "origin", foreignBare)

	for _, command := range [][]string{{"push"}, {"sync"}} {
		code, _, stderr := run(t, seed, command...)
		if code == 0 {
			t.Fatalf("%v into a foreign project exited 0, want a refusal", command)
		}
		for _, want := range []string{projectIDOf(t, seed), foreignID, "refs/workbook/project"} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("%v stderr %q does not name %q", command, stderr, want)
			}
		}
	}
	if got := cliGitOutput(t, foreignBare, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/"); got != "" {
		t.Fatalf("foreign origin holds %q, want no task ref from another project", got)
	}
	if remoteHasTaskRef(t, seed, task.ID) {
		t.Fatalf("task %s was published into another project's repository", task.ID)
	}
}

// Forks do not copy Workbook refs, but they do copy files, and the committed
// advisory identity is a file. Setup in a fork therefore inherits the upstream
// project rather than minting a new one — the opposite of what "refs do not
// travel" suggests — and starting fresh means removing what would be adopted.
// The README states both; this is what makes that statement true.
func TestForkInheritsUpstreamIdentityUnlessTheAdvisoryCopyIsRemoved(t *testing.T) {
	_, upstream, _ := originAdoptedAfterClone(t)
	if code, _, stderr := run(t, upstream, "setup"); code != 0 {
		t.Fatalf("upstream setup code = %d, want 0; stderr = %q", code, stderr)
	}
	cliGit(t, upstream, "add", ".workbook/config.json")
	cliGit(t, upstream, "commit", "--quiet", "-m", "Initialize Workbook")
	cliGit(t, upstream, "push", "--quiet", "origin", "main")
	upstreamID := projectIDOf(t, upstream)

	// A fork: the branches, not the refs, copied into a separate remote.
	forkBare := filepath.Join(t.TempDir(), "fork.git")
	cliGit(t, t.TempDir(), "init", "--bare", "--quiet", forkBare)
	fork := filepath.Join(t.TempDir(), "fork")
	cliGit(t, t.TempDir(), "clone", "--quiet", upstream, fork)
	cliGit(t, fork, "config", "user.name", "Workbook Test")
	cliGit(t, fork, "config", "user.email", "workbook@example.test")
	cliGit(t, fork, "remote", "set-url", "origin", forkBare)
	cliGit(t, fork, "push", "--quiet", "origin", "HEAD:refs/heads/main")

	code, stdout, stderr := run(t, fork, "setup")
	if code != 0 {
		t.Fatalf("fork setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := projectID(t, stdout); got != upstreamID {
		t.Fatalf("fork project = %q, want the inherited upstream project %q", got, upstreamID)
	}
	if got := cliGitOutput(t, forkBare, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/"); got != "" {
		t.Fatalf("fork remote holds task refs %q, want none: refs do not travel with a fork", got)
	}

	// The documented way to start an independent project in a fork.
	fresh := filepath.Join(t.TempDir(), "fresh")
	cliGit(t, t.TempDir(), "clone", "--quiet", upstream, fresh)
	cliGit(t, fresh, "config", "user.name", "Workbook Test")
	cliGit(t, fresh, "config", "user.email", "workbook@example.test")
	cliGit(t, fresh, "rm", "--quiet", ".workbook/config.json")
	code, stdout, stderr = run(t, fresh, "setup", "--no-sync")
	if code != 0 {
		t.Fatalf("fresh fork setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := projectID(t, stdout); got == upstreamID {
		t.Fatalf("fresh fork project = %q, want a newly minted project", got)
	}
	if got, want := projectIdentitySource(t, stdout), "(minted and published)"; got != want {
		t.Fatalf("fresh fork identity source = %q, want %q", got, want)
	}
}

// A server-side policy can accept task refs and refuse the identity ref beside
// them. Publication deliberately continues — nothing on that remote claims a
// project, which is where every pre-v0.5.0 remote already stands — but the
// result is a project accumulating task refs with no identity, which is exactly
// what leaves a later bare-branch clone with nothing to join. Every publishing
// command therefore has to say so, on its own channel, while still succeeding.
func TestPublicationWarnsWhenOriginRefusesTheIdentityRef(t *testing.T) {
	bare, seed, _ := originAdoptedAfterClone(t)
	hook := "#!/bin/sh\nwhile read old new ref; do\n" +
		"  if [ \"$ref\" = \"refs/workbook/project\" ]; then\n" +
		"    echo 'identity refs are not accepted here' >&2\n    exit 1\n  fi\ndone\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}

	code, stdout, stderr := run(t, seed, "setup")
	if code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "Identity:\t") ||
		!strings.Contains(stdout, "could not publish refs/workbook/project to origin") ||
		!strings.Contains(stdout, "identity refs are not accepted here") {
		t.Fatalf("setup stdout = %q, want the refused publication reported", stdout)
	}

	// A mutation: the warning rides stderr in text mode and the sync member in
	// JSON, and the change itself is published either way.
	code, _, stderr = run(t, seed, "create", "Recorded despite the refusal")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	assertIdentityRefusalWarning(t, "create", stderr)
	code, stdout, stderr = run(t, seed, "create", "Recorded again", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("create --json = (%d, %q); want 0 with the warning in the envelope", code, stderr)
	}
	assertIdentityRefusalMember(t, "create --json", stdout)

	for _, command := range []string{"push", "sync"} {
		code, _, stderr = run(t, seed, command)
		if code != 0 {
			t.Fatalf("%s code = %d, want 0; stderr = %q", command, code, stderr)
		}
		assertIdentityRefusalWarning(t, command, stderr)

		code, stdout, stderr = run(t, seed, command, "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("%s --json = (%d, %q); want 0 with the warning in the envelope", command, code, stderr)
		}
		assertIdentityRefusalMember(t, command+" --json", stdout)
	}

	// The carve-out itself is unchanged: the work is published, the identity is
	// not, and nothing was refused to the user.
	if got := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/workbook/project"); got != "" {
		t.Fatalf("origin identity refs = %q, want none past the hook", got)
	}
	if got := cliGitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/workbook/tasks/"); got == "" {
		t.Fatal("origin holds no task refs, so the carve-out did not let publication through")
	}
}

// assertIdentityRefusalWarning insists the warning is one line that names the
// ref and quotes origin's own words, so a user can tell which remote policy to
// go and change.
func assertIdentityRefusalWarning(t *testing.T, command, stderr string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		if !strings.HasPrefix(line, "workbook: warning: ") {
			continue
		}
		if strings.Contains(line, "could not publish refs/workbook/project to origin") &&
			strings.Contains(line, "identity refs are not accepted here") {
			return
		}
	}
	t.Fatalf("%s stderr = %q, want one warning line naming the ref and origin's refusal", command, stderr)
}

func assertIdentityRefusalMember(t *testing.T, command, stdout string) {
	t.Helper()
	for _, want := range []string{
		`"identity":{`,
		`"could not publish refs/workbook/project to origin`,
		`identity refs are not accepted here`,
		`"unpublished":true`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("%s stdout = %q, want an identity member containing %q", command, stdout, want)
		}
	}
}

// A checkout whose branch carries no tracked configuration is now usable: the
// identity ref says which project it is. The missing advisory copy is worth one
// line on stderr and nothing more.
func TestCommandsReportMissingTrackedConfigurationOnceAndKeepWorking(t *testing.T) {
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Recorded before the branch switch")
	if err := os.Remove(filepath.Join(repository, ".workbook", "config.json")); err != nil {
		t.Fatalf("remove tracked configuration: %v", err)
	}

	code, stdout, stderr := run(t, repository, "list", "--json")
	if code != 0 {
		t.Fatalf("list code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, task.ID) {
		t.Fatalf("list stdout = %q, want task %s", stdout, task.ID)
	}
	warnings := 0
	for _, line := range strings.Split(strings.TrimSuffix(stderr, "\n"), "\n") {
		if strings.HasPrefix(line, "workbook: warning: ") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("list stderr = %q, want exactly one warning line", stderr)
	}
	if !strings.Contains(stderr, "refs/workbook/project") || !strings.Contains(stderr, ".workbook/config.json") {
		t.Fatalf("list stderr = %q, want it to name the ref and the missing file", stderr)
	}
}

// Every caller that parses sync's JSON reads a fixed envelope. The identity
// stage must add a member only when it has something to report, so a
// steady-state run stays byte-identical to what earlier versions emitted.
func TestSyncJSONOmitsIdentityWhenNothingChanged(t *testing.T) {
	_, seed, _ := originAdoptedAfterClone(t)
	// --no-sync keeps bootstrap local, so the publication origin needs is left
	// for the first explicit sync to make.
	if code, _, stderr := run(t, seed, "setup", "--no-sync"); code != 0 {
		t.Fatalf("setup code = %d, want 0; stderr = %q", code, stderr)
	}

	code, first, stderr := run(t, seed, "sync", "--json")
	if code != 0 {
		t.Fatalf("first sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(first, `"identity"`) {
		t.Fatalf("first sync JSON = %q, want the one-time identity publication reported", first)
	}

	code, second, stderr := run(t, seed, "sync", "--json")
	if code != 0 {
		t.Fatalf("second sync code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.Contains(second, `"identity"`) {
		t.Fatalf("second sync JSON = %q, want no identity member once origin agrees", second)
	}
	var envelope struct {
		Data struct {
			Remote string          `json:"remote"`
			Fetch  json.RawMessage `json:"fetch"`
			Push   json.RawMessage `json:"push"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(second), &envelope); err != nil {
		t.Fatalf("decode sync result: %v; output = %q", err, second)
	}
	if envelope.Data.Remote != "origin" || len(envelope.Data.Fetch) == 0 || len(envelope.Data.Push) == 0 {
		t.Fatalf("sync result = %q, want the unchanged remote, fetch and push members", second)
	}
}
