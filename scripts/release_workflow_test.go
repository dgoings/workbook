package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	Name string `yaml:"name"`
	On   struct {
		Push struct {
			Tags []string `yaml:"tags"`
		} `yaml:"push"`
		PullRequest struct {
			Types []string `yaml:"types"`
		} `yaml:"pull_request"`
		// WorkflowCall and WorkflowDispatch are `any` because both are valid as
		// an empty mapping, and only their presence is asserted here.
		WorkflowCall     any `yaml:"workflow_call"`
		WorkflowDispatch any `yaml:"workflow_dispatch"`
	} `yaml:"on"`
	Concurrency struct {
		Group string `yaml:"group"`
	} `yaml:"concurrency"`
	Jobs map[string]releaseJob `yaml:"jobs"`
}

type releaseJob struct {
	Name        string            `yaml:"name"`
	If          string            `yaml:"if"`
	Uses        string            `yaml:"uses"`
	Needs       any               `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []releaseStep     `yaml:"steps"`
}

type releaseStep struct {
	Name string `yaml:"name"`
	Uses string `yaml:"uses"`
	Run  string `yaml:"run"`
	With struct {
		FetchDepth any    `yaml:"fetch-depth"`
		Ref        string `yaml:"ref"`
	} `yaml:"with"`
}

// A tag pushed with the default GITHUB_TOKEN does not create a new workflow
// run. Without the workflow_call trigger the two cut workflows would tag a
// commit and publish nothing, silently.
func TestReleaseWorkflowIsReachableByTagPushAndByCall(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release.yml")

	if !containsString(workflow.On.Push.Tags, "v*") {
		t.Errorf("push tags = %v, want a v* tag trigger for locally pushed tags", workflow.On.Push.Tags)
	}
	if workflow.On.WorkflowCall == nil {
		t.Error("release workflow has no workflow_call trigger, so a workflow-pushed tag would publish nothing")
	}
}

// A called run's github.ref is the caller's branch. Grouping on it would put
// every called release in one group named for main, so a release would queue
// behind an unrelated one.
func TestReleaseWorkflowGroupsConcurrencyByTag(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release.yml")

	if !strings.Contains(workflow.Concurrency.Group, "inputs.tag") {
		t.Errorf("concurrency group = %q, want it keyed on the release tag", workflow.Concurrency.Group)
	}
}

// Production mutation: publishing the caller's branch rather than the tag would
// build archives from whatever main happened to contain.
func TestReleaseWorkflowChecksOutTheTagItPublishes(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release.yml")
	job, ok := workflow.Jobs["release"]
	if !ok {
		t.Fatalf("release workflow jobs = %v, want a release job", keysOf(workflow.Jobs))
	}

	var checkout releaseStep
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			checkout = step
			break
		}
	}
	if checkout.Uses == "" {
		t.Fatal("release job never checks out a ref")
	}
	if !strings.Contains(checkout.With.Ref, "inputs.tag") {
		t.Errorf("checkout ref = %q, want the called tag to win over the pushed ref", checkout.With.Ref)
	}
}

// Both cut workflows have to reach publication by calling the release workflow.
// Relying on their tag push to trigger it is the failure this design exists to
// avoid, and it fails silently rather than loudly.
func TestCutWorkflowsCallTheReleaseWorkflow(t *testing.T) {
	for _, name := range []string{"cut-release.yml", "release-pr.yml"} {
		t.Run(name, func(t *testing.T) {
			workflow := readReleaseWorkflow(t, name)
			publish, ok := workflow.Jobs["publish"]
			if !ok {
				t.Fatalf("%s jobs = %v, want a publish job", name, keysOf(workflow.Jobs))
			}
			if publish.Uses != "./.github/workflows/release.yml" {
				t.Errorf("publish job uses %q, want the reusable release workflow", publish.Uses)
			}
			if publish.Needs == nil {
				t.Error("publish job does not wait for the tag to be pushed")
			}
			if got := publish.Permissions["contents"]; got != "write" {
				t.Errorf("publish job contents permission = %q, want write so the called workflow can publish", got)
			}
		})
	}
}

// Production mutation: cutting on any closed pull request would release work
// from every pull request someone abandoned, and dropping the repository guard
// would let a fork's pull request reach a job holding write permission.
func TestReleasePullRequestCutIsGuarded(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release-pr.yml")
	cut, ok := workflow.Jobs["cut"]
	if !ok {
		t.Fatalf("release-pr jobs = %v, want a cut job", keysOf(workflow.Jobs))
	}

	for _, guard := range []string{
		"github.event.pull_request.merged == true",
		"github.event.pull_request.base.ref == 'main'",
		"github.event.pull_request.head.repo.full_name == github.repository",
	} {
		if !strings.Contains(cut.If, guard) {
			t.Errorf("cut job condition %q is missing the guard %q", cut.If, guard)
		}
	}
}

// The check runs on every pull request so it can be marked required. Restricting
// it to labelled ones would leave every ordinary pull request waiting on a check
// that never reports.
func TestReleasePullRequestValidationRunsOnEveryPullRequest(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release-pr.yml")
	validate, ok := workflow.Jobs["validate"]
	if !ok {
		t.Fatalf("release-pr jobs = %v, want a validate job", keysOf(workflow.Jobs))
	}

	if strings.Contains(validate.If, "labels") {
		t.Errorf("validate condition %q filters on labels, so unlabelled pull requests never report", validate.If)
	}
	if got := validate.Permissions["contents"]; got != "read" {
		t.Errorf("validate job contents permission = %q, want read", got)
	}
	for _, action := range []string{"labeled", "unlabeled", "synchronize"} {
		if !containsString(workflow.On.PullRequest.Types, action) {
			t.Errorf("pull_request types = %v, want %q so the check reruns when it changes", workflow.On.PullRequest.Types, action)
		}
	}
}

// Resolving the next version and reading the changelog both need the full tag
// list, which the default shallow checkout does not carry. A shallow clone would
// find no previous release and restart numbering from zero.
func TestCutWorkflowsCheckOutTheFullTagHistory(t *testing.T) {
	for _, name := range []string{"cut-release.yml", "release-pr.yml"} {
		t.Run(name, func(t *testing.T) {
			workflow := readReleaseWorkflow(t, name)
			for jobName, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if !strings.HasPrefix(step.Uses, "actions/checkout@") {
						continue
					}
					if depth, ok := step.With.FetchDepth.(int); !ok || depth != 0 {
						t.Errorf("job %q checkout fetch-depth = %v, want 0 for the full tag list", jobName, step.With.FetchDepth)
					}
				}
			}
		})
	}
}

// Production mutation: a floating action reference lets a third party change
// what runs in a job holding write permission and the tap credential.
func TestReleaseWorkflowsPinActionsAndRunners(t *testing.T) {
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)

	for _, name := range []string{"release.yml", "cut-release.yml", "release-pr.yml"} {
		t.Run(name, func(t *testing.T) {
			workflow := readReleaseWorkflow(t, name)
			for jobName, job := range workflow.Jobs {
				for _, step := range job.Steps {
					if step.Uses == "" {
						continue
					}
					if !pinned.MatchString(step.Uses) {
						t.Errorf("job %q step %q uses %q, want a full commit SHA", jobName, step.Name, step.Uses)
					}
				}
			}
			contents := readReleaseWorkflowFile(t, name)
			for _, forbidden := range []string{"ubuntu-latest", "macos-latest"} {
				if strings.Contains(contents, forbidden) {
					t.Errorf("%s pins the moving runner label %q", name, forbidden)
				}
			}
		})
	}
}

// The scripts the workflows call have to exist and be executable, since a typo
// in a run step is only discovered when a release is attempted.
func TestReleaseWorkflowsCallScriptsThatExist(t *testing.T) {
	root, _ := renderFormulaPaths(t)
	referenced := regexp.MustCompile(`scripts/[a-z-]+\.sh`)

	for _, name := range []string{"release.yml", "cut-release.yml", "release-pr.yml"} {
		t.Run(name, func(t *testing.T) {
			contents := readReleaseWorkflowFile(t, name)
			matches := referenced.FindAllString(contents, -1)
			if len(matches) == 0 {
				t.Fatalf("%s references no scripts", name)
			}
			for _, match := range matches {
				info, err := os.Stat(filepath.Join(root, match))
				if err != nil {
					t.Errorf("%s calls %s, which does not exist: %v", name, match, err)
					continue
				}
				if info.Mode()&0o111 == 0 {
					t.Errorf("%s calls %s, which is not executable", name, match)
				}
			}
		})
	}
}

func readReleaseWorkflow(t *testing.T, name string) releaseWorkflow {
	t.Helper()
	var workflow releaseWorkflow
	if err := yaml.Unmarshal([]byte(readReleaseWorkflowFile(t, name)), &workflow); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return workflow
}

func readReleaseWorkflowFile(t *testing.T, name string) string {
	t.Helper()
	root, _ := renderFormulaPaths(t)
	contents, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func keysOf(jobs map[string]releaseJob) []string {
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	return names
}
