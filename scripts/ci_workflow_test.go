package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/testenv"
	"gopkg.in/yaml.v3"
)

type ciWorkflow struct {
	Name string `yaml:"name"`
	On   struct {
		Push struct {
			Branches []string `yaml:"branches"`
			Tags     []string `yaml:"tags"`
			// Paths and PathsIgnore have to stay empty; see
			// TestCIWorkflowGatesTheExpensiveStepsRatherThanTheJob.
			Paths       []string `yaml:"paths"`
			PathsIgnore []string `yaml:"paths-ignore"`
		} `yaml:"push"`
		PullRequest struct {
			Branches    []string `yaml:"branches"`
			Paths       []string `yaml:"paths"`
			PathsIgnore []string `yaml:"paths-ignore"`
		} `yaml:"pull_request"`
	} `yaml:"on"`
	Concurrency struct {
		Group string `yaml:"group"`
		// CancelInProgress is `any` because YAML reads a bare `true` as a bool
		// and a `${{ }}` expression as a string, and the difference is the
		// point of TestCIWorkflowNeverCancelsAPushVerification.
		CancelInProgress any `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]ciJob `yaml:"jobs"`
}

type ciJob struct {
	RunsOn string `yaml:"runs-on"`
	// If carries the job-level condition, which has to stay empty: a job that
	// does not run reports no status, and these are required checks.
	If       string `yaml:"if"`
	Strategy struct {
		Matrix struct {
			OS []string `yaml:"os"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []ciStep `yaml:"steps"`
}

type ciStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

// steps returns the single verification job's steps, failing when the workflow
// does not describe one.
func (w ciWorkflow) job(t *testing.T) ciJob {
	t.Helper()
	if len(w.Jobs) != 1 {
		t.Fatalf("workflow defines %d jobs, want exactly one verification job", len(w.Jobs))
	}
	for _, job := range w.Jobs {
		return job
	}
	return ciJob{}
}

// commands concatenates every `run` script in the job, which is where the
// workflow's actual verification lives.
func (j ciJob) commands() string {
	var commands strings.Builder
	for _, step := range j.Steps {
		commands.WriteString(step.Run)
		commands.WriteString("\n")
	}
	return commands.String()
}

// Production mutation: a workflow that only fires on tags, like release.yml,
// leaves every pull request unverified, which is the gap this workflow closes.
func TestCIWorkflowRunsOnPushAndPullRequestAgainstMain(t *testing.T) {
	workflow := readCIWorkflow(t)

	if got := workflow.On.Push.Branches; len(got) != 1 || got[0] != "main" {
		t.Errorf("push branches = %v, want [main]", got)
	}
	if got := workflow.On.Push.Tags; len(got) != 0 {
		t.Errorf("push tags = %v, want none; releases are the release workflow's job", got)
	}
	if got := workflow.On.PullRequest.Branches; len(got) != 1 || got[0] != "main" {
		t.Errorf("pull_request branches = %v, want [main]", got)
	}
}

// Production mutation: `cancel-in-progress: true` on a group keyed only by
// github.ref makes every push to main share one group, so each merge discards
// the previous merge's verification. That happened: of the six main pushes
// following this workflow's own merge, five were cancelled during "Set up job"
// having run no tests at all, and the one that survived failed -- proving main
// can break from a merge even when every pull request run was green, which is
// the entire reason the push trigger exists.
//
// Cancellation is not the only way a run is lost. GitHub also cancels a run
// left *pending* in a group when a newer run queues behind the same group, so
// pushes have to be keyed on the commit rather than merely opting out of
// cancel-in-progress.
func TestCIWorkflowNeverCancelsAPushVerification(t *testing.T) {
	workflow := readCIWorkflow(t)
	group := workflow.Concurrency.Group

	if group == "" {
		t.Fatal("workflow declares no concurrency group")
	}
	// A group that does not vary per commit puts consecutive main pushes in
	// one group, where the newer run evicts the older one.
	if !strings.Contains(group, "github.sha") {
		t.Errorf("concurrency group %q does not vary per commit, so consecutive "+
			"pushes to main share a group and evict each other", group)
	}

	switch cancel := workflow.Concurrency.CancelInProgress.(type) {
	case nil:
	case bool:
		if cancel {
			t.Error("cancel-in-progress is unconditionally true, so a merge to " +
				"main cancels the previous merge's verification")
		}
	case string:
		// The only safe form is one that turns cancellation off for pushes,
		// which means deciding on the event.
		if !strings.Contains(cancel, "github.event_name") {
			t.Errorf("cancel-in-progress = %q, want an expression conditioned on "+
				"github.event_name so pushes are never cancelled", cancel)
		}
		if !strings.Contains(cancel, "pull_request") {
			t.Errorf("cancel-in-progress = %q, want cancellation limited to "+
				"pull_request events", cancel)
		}
	default:
		t.Errorf("cancel-in-progress has unexpected type %T (%v)", cancel, cancel)
	}
}

// Production mutation: dropping any of these three checks lets unformatted,
// suspect, or failing code reach main with a green tick.
func TestCIWorkflowRunsTestsVetAndFormatVerification(t *testing.T) {
	job := readCIWorkflow(t).job(t)
	commands := job.commands()

	for _, want := range []string{"go test ./...", "go vet ./...", "gofmt -l ."} {
		if !strings.Contains(commands, want) {
			t.Errorf("workflow never runs %q:\n%s", want, commands)
		}
	}
	// gofmt reports unformatted files on stdout and still exits 0, so the
	// workflow has to inspect its output to fail.
	if !strings.Contains(commands, "unformatted") || !strings.Contains(commands, "exit 1") {
		t.Errorf("workflow does not fail on gofmt output:\n%s", commands)
	}
}

// Production mutation: without a provisioned node the 36 embedded client
// behavior tests skip and the package still reports ok.
func TestCIWorkflowProvisionsNodeAndFailsWhenCapabilitiesAreMissing(t *testing.T) {
	job := readCIWorkflow(t).job(t)

	var setupNode ciStep
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-node@") {
			setupNode = step
		}
	}
	if setupNode.Uses == "" {
		t.Fatal("workflow never provisions node with actions/setup-node")
	}
	if setupNode.With["node-version"] == nil {
		t.Errorf("actions/setup-node step does not pin a node-version: %+v", setupNode.With)
	}

	commands := job.commands()
	if !strings.Contains(commands, "scripts/check-ci-capabilities.sh") {
		t.Errorf("workflow does not run the capability preflight:\n%s", commands)
	}
	if _, err := os.Stat(filepath.Join(repositoryRootForCI(t), "scripts", "check-ci-capabilities.sh")); err != nil {
		t.Errorf("capability preflight is missing: %v", err)
	}

	var requirement string
	var found bool
	for _, step := range job.Steps {
		if value, ok := step.Env[testenv.RequireCapabilitiesVariable]; ok {
			requirement, found = value, true
		}
	}
	if !found || requirement == "" {
		t.Errorf("no step sets %s, so a missing capability would skip instead of failing",
			testenv.RequireCapabilitiesVariable)
	}
}

// Production mutation: losing the skip report, or the pipefail that preserves
// `go test`'s exit status through it, hides exactly the shrinking suite the
// report exists to expose.
func TestCIWorkflowReportsSkipsWithoutSwallowingTestFailures(t *testing.T) {
	commands := readCIWorkflow(t).job(t).commands()

	for _, want := range []string{"-json", "./scripts/skipreport", "set -o pipefail"} {
		if !strings.Contains(commands, want) {
			t.Errorf("workflow test step is missing %q:\n%s", want, commands)
		}
	}
}

// Production mutation: verifying one operating system leaves the other
// published platform covered only by whichever machine a developer happens to
// use, which so far has been darwin/arm64 alone.
func TestCIWorkflowVerifiesBothPublishedPlatforms(t *testing.T) {
	job := readCIWorkflow(t).job(t)

	if job.RunsOn != "${{ matrix.os }}" {
		t.Errorf("runs-on = %q, want the operating-system matrix", job.RunsOn)
	}
	for _, want := range []string{"ubuntu-24.04", "macos-15"} {
		if !containsString(job.Strategy.Matrix.OS, want) {
			t.Errorf("matrix os = %v, want an entry for %q", job.Strategy.Matrix.OS, want)
		}
	}
}

// Production mutation: a floating action reference lets a third party change
// what runs in CI, and a floating runner label silently moves the platform.
func TestCIWorkflowPinsActionsAndRunners(t *testing.T) {
	workflow := readCIWorkflow(t)
	job := workflow.job(t)
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)

	for _, step := range job.Steps {
		if step.Uses == "" {
			continue
		}
		if !pinned.MatchString(step.Uses) {
			t.Errorf("step %q uses %q, want a full commit SHA", step.Name, step.Uses)
		}
	}
	contents := readCIWorkflowFile(t)
	for _, forbidden := range []string{"ubuntu-latest", "macos-latest"} {
		if strings.Contains(contents, forbidden) {
			t.Errorf("workflow pins the moving runner label %q:\n%s", forbidden, contents)
		}
	}
}

func readCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	var workflow ciWorkflow
	if err := yaml.Unmarshal([]byte(readCIWorkflowFile(t)), &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	return workflow
}

func readCIWorkflowFile(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repositoryRootForCI(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	return string(contents)
}

func repositoryRootForCI(t *testing.T) string {
	t.Helper()
	root, _ := checkCapabilitiesPaths(t)
	return root
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// expensiveCIWorkflowSteps names the steps a site-only change has no reason to
// pay for: two toolchain installs, the capability preflight, and the three
// verification commands. Every other step in the job is cheap enough to run
// unconditionally, and the checkout has to, because the gate diffs the tree it
// produces.
var expensiveCIWorkflowSteps = []string{
	"Set up Go",
	"Set up Node",
	"Verify test capabilities",
	"Check formatting",
	"Vet",
	"Test",
}

// Production mutation: filtering the triggers with paths-ignore, or hanging an
// `if:` on the job, stops `Verify on ubuntu-24.04` and `Verify on macos-15`
// from reporting at all. Both are required checks, so a site-only pull request
// would then sit blocked forever waiting for a status GitHub was never going to
// send -- a worse outcome than the wasted minutes the skip is meant to save.
// The job has to start and report on every event; only the steps inside it may
// be skipped.
func TestCIWorkflowGatesTheExpensiveStepsRatherThanTheJob(t *testing.T) {
	workflow := readCIWorkflow(t)
	job := workflow.job(t)
	gate, decision := ciWorkflowGate(t, job)

	if job.If != "" {
		t.Errorf("job is conditional on %q, so a skipped run never reports the required checks", job.If)
	}
	for name, filter := range map[string][]string{
		"push paths":                workflow.On.Push.Paths,
		"push paths-ignore":         workflow.On.Push.PathsIgnore,
		"pull_request paths":        workflow.On.PullRequest.Paths,
		"pull_request paths-ignore": workflow.On.PullRequest.PathsIgnore,
	} {
		if len(filter) != 0 {
			t.Errorf("workflow filters its triggers with %s = %v, so a filtered change "+
				"produces no run and the required checks are never reported", name, filter)
		}
	}
	if gate.If != "" {
		t.Errorf("the deciding step is itself conditional on %q, so it cannot decide anything", gate.If)
	}

	gateIndex := -1
	for index, step := range job.Steps {
		if step.ID == gate.ID {
			gateIndex = index
		}
		// A gated checkout would leave the gate with no history to diff, and a
		// gated decision cannot be reached at all.
		if strings.HasPrefix(step.Uses, "actions/checkout@") && step.If != "" {
			t.Errorf("the checkout is conditional on %q, but the gate needs the "+
				"fetched history to compute a diff", step.If)
		}
	}

	// The condition is spelled out rather than merely searched for, because
	// GitHub's expression parser reads a hyphen as subtraction: an output named
	// `code-changed` would silently evaluate to nothing and skip every gated
	// step, including on a change to the Go program.
	want := "steps." + gate.ID + ".outputs." + decision + " == 'true'"
	for _, name := range expensiveCIWorkflowSteps {
		index, step := ciWorkflowStep(t, job, name)
		if step.If != want {
			t.Errorf("step %q is conditional on %q, want %q", name, step.If, want)
		}
		if index < gateIndex {
			t.Errorf("step %q runs before the deciding step, so its condition reads an "+
				"output that does not exist yet", name)
		}
	}
}

// Production mutation: widening the exemption to `**.md` or `docs/**` is the
// tempting next edit, because prose looks as inert as the site's HTML. It is
// not. The suite asserts on what README.md, CONTRIBUTING.md, docs/reference.md
// and docs/architecture.md say, so a documentation change that skipped it would
// land exactly the divergence those tests exist to catch. The static site and
// its Render blueprint are the whole exemption, and nothing else in the
// repository may join them.
func TestCIWorkflowExemptsOnlyTheStaticSiteFromVerification(t *testing.T) {
	gate, _ := ciWorkflowGate(t, readCIWorkflow(t).job(t))
	// Only the executable part is examined: the prose above it explains which
	// paths are deliberately absent, and naming them there is not exempting
	// them. Backslashes come out with the comments, because a path matched as a
	// pattern spells its dots `render\.yaml` and this is asking which files the
	// gate names, not how it spells them.
	script := strings.ReplaceAll(shellWithoutComments(gate.Run), `\`, "")

	for _, want := range []string{"site/", "render.yaml"} {
		if !strings.Contains(script, want) {
			t.Errorf("the gate never mentions %q, so it cannot recognise a site-only change:\n%s", want, script)
		}
	}
	entries, err := os.ReadDir(repositoryRootForCI(t))
	if err != nil {
		t.Fatalf("read repository root: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// Tooling directories are not part of the shipped tree and are named
		// nowhere in the gate; skipping them keeps the check to the paths a
		// contributor could plausibly want exempted.
		if strings.HasPrefix(name, ".") || name == "site" || name == "render.yaml" {
			continue
		}
		if strings.Contains(script, name) {
			t.Errorf("the gate names %q, which is not part of the static site; only "+
				"site/ and render.yaml may skip the verification:\n%s", name, script)
		}
	}
}

// Production mutation: a gate that guesses wrong towards skipping puts a green
// tick on a commit no runner compiled, which is strictly worse than the waste
// it replaces. So every way of failing to establish a diff -- an event with no
// base commit, the all-zero commit a branch-creating push reports, a base that
// a force push removed from the history, an empty diff, an event this does not
// model -- has to resolve to running everything, and only a diff positively
// confined to site/ and render.yaml may skip. Reading the script cannot show
// which way it falls, so the workflow's own script is executed here against a
// repository built to pose each question.
func TestCIWorkflowSiteOnlyGateFailsOpen(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		testenv.MissingCapability(t, "bash is required to execute the CI workflow's gate script")
	}
	gate, decision := ciWorkflowGate(t, readCIWorkflow(t).job(t))
	script := filepath.Join(t.TempDir(), "gate.sh")
	if err := os.WriteFile(script, []byte(gate.Run), 0o700); err != nil {
		t.Fatalf("write the gate script: %v", err)
	}
	repository, commits := ciWorkflowGateRepository(t)
	// Well-formed but absent, which is what a base commit looks like after the
	// branch carrying it was force-pushed away.
	absent := strings.Repeat("abcdef0123456789", 3)[:40]

	for _, testCase := range []struct {
		name  string
		event string
		base  string
		head  string
		want  string
	}{
		{
			name:  "a pull request touching only the site skips the verification",
			event: "pull_request", base: commits["base"], head: commits["site"], want: "false",
		},
		{
			name:  "a pull request touching Go code runs it",
			event: "pull_request", base: commits["base"], head: commits["code"], want: "true",
		},
		{
			name:  "a pull request touching documentation runs it",
			event: "pull_request", base: commits["base"], head: commits["documentation"], want: "true",
		},
		{
			// The exemption is the site directory, not every path that starts
			// with those four letters.
			name:  "a pull request touching a file merely named like the site runs it",
			event: "pull_request", base: commits["base"], head: commits["site-adjacent"], want: "true",
		},
		{
			// Three-dot semantics: the branch is judged from where it left main,
			// so commits main gathered meanwhile -- already verified by their own
			// push runs -- are not read as this branch's work.
			name:  "a site-only pull request is judged from where it branched",
			event: "pull_request", base: commits["main-ahead"], head: commits["site"], want: "false",
		},
		{
			name:  "a push of a site-only commit skips the verification",
			event: "push", base: commits["base"], head: commits["site"], want: "false",
		},
		{
			// Two-dot semantics for pushes: a force push replaces one state of the
			// branch with an unrelated one, and the tree that results is what has
			// to be verified, merge base or no merge base.
			name:  "a force push that rewrites code is verified",
			event: "push", base: commits["code"], head: commits["site"], want: "true",
		},
		{
			name:  "a push reporting the all-zero previous commit runs everything",
			event: "push", base: strings.Repeat("0", 40), head: commits["site"], want: "true",
		},
		{
			name:  "a pull request carrying no base commit runs everything",
			event: "pull_request", base: "", head: commits["site"], want: "true",
		},
		{
			name:  "a base commit missing from the history runs everything",
			event: "pull_request", base: absent, head: commits["site"], want: "true",
		},
		{
			name:  "an empty diff runs everything",
			event: "pull_request", base: commits["site"], head: commits["site"], want: "true",
		},
		{
			name:  "an event the gate does not model runs everything",
			event: "workflow_dispatch", base: commits["base"], head: commits["site"], want: "true",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runCommand(t, repository, nil, "git", "-c", "advice.detachedHead=false",
				"checkout", "--quiet", testCase.head)

			got, log := runCIWorkflowGate(t, script, repository, decision, testCase.event, testCase.base)
			if got != testCase.want {
				t.Errorf("gate decided %s=%q, want %q\n%s", decision, got, testCase.want, log)
			}
		})
	}
}

// ciWorkflowGate returns the step that decides whether the verification runs --
// the one step writing to $GITHUB_OUTPUT -- along with the name of the output
// the rest of the job reads. Both are discovered rather than assumed, so the
// tests describe the arrangement instead of pinning one spelling of it.
func ciWorkflowGate(t *testing.T, job ciJob) (ciStep, string) {
	t.Helper()
	var deciding []ciStep
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "GITHUB_OUTPUT") {
			deciding = append(deciding, step)
		}
	}
	if len(deciding) != 1 {
		t.Fatalf("%d steps write to $GITHUB_OUTPUT, want exactly one deciding step", len(deciding))
	}
	gate := deciding[0]
	if gate.ID == "" {
		t.Fatalf("the deciding step %q declares no id, so no other step can read its decision", gate.Name)
	}
	// Only underscores and alphanumerics, because that is all GitHub can
	// dereference in an expression without index syntax.
	reference := regexp.MustCompile(`steps\.` + regexp.QuoteMeta(gate.ID) + `\.outputs\.([A-Za-z0-9_]+)`)
	for _, step := range job.Steps {
		if match := reference.FindStringSubmatch(step.If); match != nil {
			return gate, match[1]
		}
	}
	t.Fatalf("no step is conditional on the decision of step %q, so nothing is gated", gate.ID)
	return ciStep{}, ""
}

// ciWorkflowStep returns the named step and its position in the job.
func ciWorkflowStep(t *testing.T, job ciJob, name string) (int, ciStep) {
	t.Helper()
	for index, step := range job.Steps {
		if step.Name == name {
			return index, step
		}
	}
	t.Fatalf("workflow has no step named %q", name)
	return 0, ciStep{}
}

// shellWithoutComments drops the comment lines from a shell script, leaving the
// part that actually decides anything.
func shellWithoutComments(script string) string {
	var code strings.Builder
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	return code.String()
}

// ciWorkflowGateRepository builds a repository posing the questions the gate has
// to answer: a main branch that keeps moving, and branches off it that change
// only the site, only Go code, and only documentation.
func ciWorkflowGateRepository(t *testing.T) (string, map[string]string) {
	t.Helper()
	repository := t.TempDir()
	runCommand(t, repository, nil, "git", "init", "--quiet", "--initial-branch=main")
	runCommand(t, repository, nil, "git", "config", "user.name", "CI Workflow Test")
	runCommand(t, repository, nil, "git", "config", "user.email", "ci-workflow-test@example.com")

	commits := make(map[string]string, 5)
	commit := func(name string, files map[string]string) {
		for path, contents := range files {
			absolute := filepath.Join(repository, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				t.Fatalf("create %s: %v", path, err)
			}
			if err := os.WriteFile(absolute, []byte(contents), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
		runCommand(t, repository, nil, "git", "add", "--all")
		runCommand(t, repository, nil, "git", "commit", "--quiet", "-m", name)
		commits[name] = gitOutput(t, repository, "rev-parse", "HEAD")
	}

	commit("base", map[string]string{
		"internal/cli/run.go": "package cli\n",
		"docs/reference.md":   "# Reference\n",
		"site/index.html":     "<!doctype html>\n",
		"render.yaml":         "services: []\n",
		"sitemap.txt":         "/\n",
	})
	for _, branch := range []struct {
		name  string
		files map[string]string
	}{
		// A file whose name merely begins with "site" is not the site, and the
		// blueprint is exempt only at the root.
		{name: "site", files: map[string]string{
			"site/index.html": "<!doctype html>\n<title>Workbook</title>\n",
			"site/README.md":  "# Site\n",
			"render.yaml":     "services: [static]\n",
		}},
		{name: "site-adjacent", files: map[string]string{"sitemap.txt": "/\n/docs\n"}},
		{name: "code", files: map[string]string{"internal/cli/run.go": "package cli\n\nfunc Run() {}\n"}},
		{name: "documentation", files: map[string]string{"docs/reference.md": "# Reference\n\n## Commands\n"}},
		{name: "main-ahead", files: map[string]string{"internal/cli/board.go": "package cli\n"}},
	} {
		runCommand(t, repository, nil, "git", "-c", "advice.detachedHead=false",
			"checkout", "--quiet", "-b", branch.name, commits["base"])
		commit(branch.name, branch.files)
	}
	return repository, commits
}

// runCIWorkflowGate executes the gate script the way a runner does, with the
// event's environment set and a fresh $GITHUB_OUTPUT, and returns the decision
// it recorded together with the log it printed.
func runCIWorkflowGate(t *testing.T, script, repository, decision, event, base string) (string, string) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatalf("create the output file: %v", err)
	}
	// GitHub defines every declared environment variable, empty where the
	// expression resolved to nothing, so the unused one is set to "" rather
	// than left out.
	pullRequestBase, pushBefore := "", ""
	switch event {
	case "pull_request":
		pullRequestBase = base
	case "push":
		pushBefore = base
	}
	command := exec.Command("bash", script)
	command.Dir = repository
	command.Env = append(os.Environ(),
		"GITHUB_OUTPUT="+output,
		"EVENT_NAME="+event,
		"PULL_REQUEST_BASE_SHA="+pullRequestBase,
		"PUSH_BEFORE_SHA="+pushBefore,
	)
	log, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the gate script failed, which would fail the whole job: %v\n%s", err, log)
	}

	recorded, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read the output file: %v", err)
	}
	var decisions []string
	for _, line := range strings.Split(string(recorded), "\n") {
		if value, found := strings.CutPrefix(line, decision+"="); found {
			decisions = append(decisions, value)
		}
	}
	// Two decisions would leave the gated steps reading whichever GitHub took
	// last, which is not a thing worth guessing about in a merge gate.
	if len(decisions) != 1 {
		t.Fatalf("gate recorded %d values for %s (%q), want exactly one\n%s",
			len(decisions), decision, recorded, log)
	}
	return decisions[0], string(log)
}
