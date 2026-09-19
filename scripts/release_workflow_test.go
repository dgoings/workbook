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
		// CancelInProgress is `any` so an absent key stays distinguishable from
		// an explicit false; a missing one would otherwise read as the safe
		// value and the assertion would pass on a workflow that never set it.
		CancelInProgress any `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]releaseJob `yaml:"jobs"`
}

type releaseJob struct {
	Name string `yaml:"name"`
	If   string `yaml:"if"`
	Uses string `yaml:"uses"`
	// Needs and Environment are `any` because each is valid as a scalar or as a
	// collection, and an absent Environment has to stay distinguishable from an
	// empty one.
	Needs       any               `yaml:"needs"`
	Environment any               `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	// Concurrency is a job's own group, which narrows the workflow's. Its
	// CancelInProgress is `any` for the same reason the workflow's is: an absent
	// key must not read as the safe value.
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress any    `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Strategy struct {
		Matrix struct {
			OS []string `yaml:"os"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	With struct {
		Tag string `yaml:"tag"`
	} `yaml:"with"`
	Steps []releaseStep `yaml:"steps"`
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

// Most merges are not releases, and a runner started only to report that it has
// nothing to do is a cost paid on every one of them. The label is checked in the
// condition so the job never starts, and again in the step because the condition
// matches a substring and cannot enforce the exactly-one-label rule.
func TestReleasePullRequestCutSkipsMergesWithNoReleaseLabel(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release-pr.yml")
	cut, ok := workflow.Jobs["cut"]
	if !ok {
		t.Fatalf("release-pr jobs = %v, want a cut job", keysOf(workflow.Jobs))
	}

	if !strings.Contains(cut.If, "labels") || !strings.Contains(cut.If, "release:") {
		t.Errorf("cut job condition %q does not gate on a release label", cut.If)
	}
	// The strict rule stays in the script: the condition cannot tell one release
	// label from two, and picking between two would cut a release of a size
	// nobody asked for.
	var checksLabelInStep bool
	for _, step := range cut.Steps {
		if strings.Contains(step.Run, "release-bump-label.sh") {
			checksLabelInStep = true
		}
	}
	if !checksLabelInStep {
		t.Error("cut job relies on its condition alone to choose a bump kind")
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

// A tag outlives the run that created it and versions only order forward, so a
// commit that fails its tests burns its version number. Both cut paths have to
// require that CI already passed before they create one.
func TestCutWorkflowsGateOnAVerifiedCommit(t *testing.T) {
	for _, testCase := range []struct {
		workflow string
		job      string
		// The commit each path can actually gate on: main's tip for a dispatch,
		// and the reviewed head for a merge, whose own merge-commit run has only
		// just been queued.
		commit string
	}{
		{workflow: "cut-release.yml", job: "tag", commit: "GITHUB_SHA"},
		{workflow: "release-pr.yml", job: "cut", commit: "REVIEWED_SHA"},
	} {
		t.Run(testCase.workflow, func(t *testing.T) {
			workflow := readReleaseWorkflow(t, testCase.workflow)
			job, ok := workflow.Jobs[testCase.job]
			if !ok {
				t.Fatalf("%s jobs = %v, want a %s job", testCase.workflow, keysOf(workflow.Jobs), testCase.job)
			}

			var gated bool
			for _, step := range job.Steps {
				if strings.Contains(step.Run, "check-commit-verified.sh") {
					gated = true
					if !strings.Contains(step.Run, testCase.commit) {
						t.Errorf("gate runs %q, want it to check ${%s}", step.Run, testCase.commit)
					}
				}
			}
			if !gated {
				t.Errorf("%s job %q creates a tag without requiring a verified commit", testCase.workflow, testCase.job)
			}
			// Reading check runs needs its own scope; without it the gate fails
			// on permissions rather than on the commit's actual state.
			if got := job.Permissions["checks"]; got != "read" {
				t.Errorf("job %q checks permission = %q, want read", testCase.job, got)
			}
		})
	}
}

// Both paths make the same decision, and making it in one place is what keeps
// the pull request check and the post-merge cut from drifting apart.
func TestCutWorkflowsPlanThroughOneScript(t *testing.T) {
	for _, name := range []string{"cut-release.yml", "release-pr.yml"} {
		t.Run(name, func(t *testing.T) {
			contents := readReleaseWorkflowFile(t, name)
			if !strings.Contains(contents, "plan-release.sh") {
				t.Errorf("%s does not plan the release through plan-release.sh", name)
			}
			// Calling the pieces directly would skip the unpublished-previous
			// check that plan-release.sh threads between them.
			for _, bypassed := range []string{"resolve-release-version.sh", "check-release-changelog.sh"} {
				if strings.Contains(contents, bypassed) {
					t.Errorf("%s calls %s directly, bypassing plan-release.sh", name, bypassed)
				}
			}
		})
	}
}

// Production mutation: a floating action reference lets a third party change
// what runs in a job holding write permission and the tap credential.
func TestReleaseWorkflowsPinActionsAndRunners(t *testing.T) {
	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)

	for _, name := range []string{"release.yml", "desktop-release.yml", "cut-release.yml", "release-pr.yml"} {
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
			for _, forbidden := range []string{"ubuntu-latest", "macos-latest", "windows-latest"} {
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

	for _, name := range []string{"release.yml", "desktop-release.yml", "cut-release.yml", "release-pr.yml"} {
		t.Run(name, func(t *testing.T) {
			contents := readReleaseWorkflowFile(t, name)
			matches := referenced.FindAllString(contents, -1)
			if len(matches) == 0 {
				t.Fatalf("%s references no scripts", name)
			}
			for _, match := range matches {
				// The desktop workflow stages the CLI by running the app's own
				// script from working-directory desktop, so a reference resolves
				// against either scripts directory. A typo still fails, because
				// it exists in neither.
				info, err := os.Stat(filepath.Join(root, match))
				if err != nil {
					info, err = os.Stat(filepath.Join(root, "desktop", match))
				}
				if err != nil {
					t.Errorf("%s calls %s, which exists in neither scripts directory: %v", name, match, err)
					continue
				}
				if info.Mode()&0o111 == 0 {
					t.Errorf("%s calls %s, which is not executable", name, match)
				}
			}
		})
	}
}

// The desktop workflow is reached the same three ways the CLI's is: a tag
// pushed by a person, a call from the cascade (a tag pushed with the default
// token starts no run), and a dispatch naming an existing tag to republish. A
// pull_request trigger would hand a contributor's branch a run ending in a job
// that holds write permission.
func TestDesktopReleaseWorkflowIsReachableByTagCallAndDispatch(t *testing.T) {
	workflow := readReleaseWorkflow(t, "desktop-release.yml")

	if len(workflow.On.Push.Tags) != 1 || workflow.On.Push.Tags[0] != "desktop-v*" {
		t.Errorf("push tags = %v, want exactly the desktop-v* sequence", workflow.On.Push.Tags)
	}
	if workflow.On.WorkflowCall == nil {
		t.Error("desktop release workflow has no workflow_call trigger, so the cascade's tag would publish nothing")
	}
	if workflow.On.WorkflowDispatch == nil {
		t.Error("desktop release workflow has no workflow_dispatch trigger, so a failed publication could not be rerun")
	}
	if strings.Contains(readReleaseWorkflowFile(t, "desktop-release.yml"), "pull_request") {
		t.Error("desktop release workflow mentions pull_request, which must never reach a job holding write permission")
	}
}

// Production mutation: publishing the caller's branch rather than the tag would
// package whatever main happened to contain and publish it under the tag's name.
func TestDesktopReleaseWorkflowChecksOutTheTagItPublishes(t *testing.T) {
	workflow := readReleaseWorkflow(t, "desktop-release.yml")

	var checkouts int
	for jobName, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/checkout@") {
				continue
			}
			checkouts++
			if !strings.Contains(step.With.Ref, "inputs.tag") {
				t.Errorf("job %q checkout ref = %q, want the called tag to win over the pushed ref", jobName, step.With.Ref)
			}
		}
	}
	if checkouts == 0 {
		t.Fatal("desktop release workflow never checks out a ref")
	}
}

// A called run's github.ref is the caller's branch, so grouping on it would put
// every cascaded desktop release in one group named for main. Cancelling is
// worse still: a publication interrupted part way can leave desktop-latest
// serving half of one build and half of another.
func TestDesktopReleaseWorkflowGroupsConcurrencyByTag(t *testing.T) {
	workflow := readReleaseWorkflow(t, "desktop-release.yml")

	if !strings.Contains(workflow.Concurrency.Group, "inputs.tag") {
		t.Errorf("concurrency group = %q, want it keyed on the desktop tag", workflow.Concurrency.Group)
	}
	if cancel, ok := workflow.Concurrency.CancelInProgress.(bool); !ok || cancel {
		t.Errorf("cancel-in-progress = %v, want an explicit false so a publication is never interrupted", workflow.Concurrency.CancelInProgress)
	}

	// Production mutation: the per-tag group keeps two runs of one release
	// apart and nothing else, while desktop-latest is one tag and one release
	// for the whole repository. Without a global group on the publishing job,
	// two different desktop tags publishing at once would each move that tag
	// and each replace its assets.
	publish, ok := workflow.Jobs["publish"]
	if !ok {
		t.Fatalf("desktop-release jobs = %v, want a publish job", keysOf(workflow.Jobs))
	}
	if publish.Concurrency.Group != "desktop-latest" {
		t.Errorf("publish job concurrency group = %q, want desktop-latest", publish.Concurrency.Group)
	}
	if cancel, ok := publish.Concurrency.CancelInProgress.(bool); !ok || cancel {
		t.Errorf("publish job cancel-in-progress = %v, want an explicit false so a publication is never interrupted", publish.Concurrency.CancelInProgress)
	}
}

// Each installer can only be built on its own platform: electron-builder makes
// a DMG on macOS, an AppImage and a deb on Linux, and an NSIS installer on
// Windows. A missing platform is a release publish-desktop-release.sh refuses
// outright, so all three runners are named, and each is pinned rather than a
// moving label that could change what a release was built on.
func TestDesktopReleaseWorkflowBuildsOnPinnedRunnersPerPlatform(t *testing.T) {
	workflow := readReleaseWorkflow(t, "desktop-release.yml")
	build, ok := workflow.Jobs["build"]
	if !ok {
		t.Fatalf("desktop-release jobs = %v, want a build job", keysOf(workflow.Jobs))
	}

	want := []string{"macos-15", "ubuntu-24.04", "windows-2025"}
	if len(build.Strategy.Matrix.OS) != len(want) {
		t.Fatalf("build matrix os = %v, want %v", build.Strategy.Matrix.OS, want)
	}
	for index, runner := range want {
		if build.Strategy.Matrix.OS[index] != runner {
			t.Errorf("build matrix os = %v, want %v", build.Strategy.Matrix.OS, want)
			break
		}
	}
}

// Every CLI release reaches desktop users, which is what the cascade is for:
// without it the app would keep bundling whichever CLI its last hand-cut
// release happened to carry. The tag is pushed and the desktop workflow called
// directly, because a tag pushed with the default token starts no run.
func TestReleaseWorkflowCascadesIntoADesktopRelease(t *testing.T) {
	workflow := readReleaseWorkflow(t, "release.yml")

	var tagJobName string
	var tagJob releaseJob
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "plan-desktop-release.sh") {
				tagJobName, tagJob = name, job
			}
		}
	}
	if tagJobName == "" {
		t.Fatalf("release workflow jobs = %v, want one planning a desktop tag", keysOf(workflow.Jobs))
	}

	if !containsString(needsNames(tagJob.Needs), "release") {
		t.Errorf("job %q needs = %v, want it to wait for the CLI release it bundles", tagJobName, tagJob.Needs)
	}
	if got := tagJob.Permissions["contents"]; got != "write" {
		t.Errorf("job %q contents permission = %q, want write so it can push the tag", tagJobName, got)
	}
	// The release job's environment approval already gated this run. A second
	// one here would park the cascade waiting on a reviewer who has approved.
	if tagJob.Environment != nil {
		t.Errorf("job %q environment = %v, want none; the release job's approval covers this run", tagJobName, tagJob.Environment)
	}
	var pushes bool
	for _, step := range tagJob.Steps {
		if strings.Contains(step.Run, "git tag") && strings.Contains(step.Run, "git push") {
			pushes = true
		}
	}
	if !pushes {
		t.Errorf("job %q plans a desktop tag without creating and pushing one", tagJobName)
	}

	var publishName string
	var publish releaseJob
	for name, job := range workflow.Jobs {
		if job.Uses == "./.github/workflows/desktop-release.yml" {
			publishName, publish = name, job
		}
	}
	if publishName == "" {
		t.Fatalf("release workflow jobs = %v, want one calling the desktop release workflow", keysOf(workflow.Jobs))
	}
	if !containsString(needsNames(publish.Needs), tagJobName) {
		t.Errorf("job %q needs = %v, want it to wait for %q to push the tag", publishName, publish.Needs, tagJobName)
	}
	if !strings.Contains(publish.With.Tag, tagJobName) {
		t.Errorf("job %q passes tag %q, want the tag %q planned", publishName, publish.With.Tag, tagJobName)
	}
	if publish.Environment != nil {
		t.Errorf("job %q environment = %v, want none; the release job's approval covers this run", publishName, publish.Environment)
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

// needs is a scalar for one dependency and a sequence for several, and a job
// that waits on the wrong thing is exactly the failure these tests exist to
// catch, so both shapes are read here rather than one of them assumed.
func needsNames(needs any) []string {
	switch value := needs.(type) {
	case string:
		return []string{value}
	case []any:
		names := make([]string, 0, len(value))
		for _, item := range value {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}

func keysOf(jobs map[string]releaseJob) []string {
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	return names
}
