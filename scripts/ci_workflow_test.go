package scripts_test

import (
	"os"
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
		} `yaml:"push"`
		PullRequest struct {
			Branches []string `yaml:"branches"`
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
	RunsOn   string `yaml:"runs-on"`
	Strategy struct {
		Matrix struct {
			OS []string `yaml:"os"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []ciStep `yaml:"steps"`
}

type ciStep struct {
	Name string            `yaml:"name"`
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
