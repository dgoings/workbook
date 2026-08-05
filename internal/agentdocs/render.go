package agentdocs

import (
	"strconv"
	"strings"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/skills"
)

// GuidelinesPath is the project-relative location of the managed guidelines.
const GuidelinesPath = ".workbook/guidelines.md"

// RenderGuidelines produces the managed guidelines body for a project. Every
// canonical value comes from the same definitions the CLI validates against, so
// there is no second list to maintain.
func RenderGuidelines(project core.ProjectConfig) string {
	var builder strings.Builder

	builder.WriteString("# Workbook guidelines\n\n")
	builder.WriteString("Workbook tracks this project's tasks in Git refs under `refs/workbook/tasks/`.\n")
	builder.WriteString("Use the `workbook` CLI as the task-state boundary. Never edit Workbook refs,\n")
	builder.WriteString("the SQLite projection, or `.workbook/config.json` directly.\n\n")

	builder.WriteString("## This project\n\n")
	builder.WriteString("| Setting | Value |\n| --- | --- |\n")
	builder.WriteString("| Project ID | `" + project.ProjectID + "` |\n")
	builder.WriteString("| Task ID prefix | `" + project.Key + "-` |\n\n")

	builder.WriteString("## Canonical statuses\n\n")
	builder.WriteString("Pass the machine value, never the display label.\n\n")
	builder.WriteString("| Machine value | Display label |\n| --- | --- |\n")
	for _, definition := range core.WorkflowStatuses() {
		builder.WriteString("| `" + string(definition.Status) + "` | " + definition.Label + " |\n")
	}
	builder.WriteString("\nWrite `--status in-progress`, not `In Progress`. The same applies to\n")
	builder.WriteString("`in-review`. A display label is rejected as a validation error.\n\n")

	builder.WriteString("## Canonical priorities\n\n")
	builder.WriteString("| Machine value | Display label |\n| --- | --- |\n")
	for _, definition := range core.Priorities() {
		builder.WriteString("| `" + string(definition.Priority) + "` | " + definition.Label + " |\n")
	}
	builder.WriteString("\n")

	builder.WriteString("## Task lifecycle\n\n")
	builder.WriteString("1. Select work with `workbook next --json`, or read a known task with\n")
	builder.WriteString("   `workbook show <id> --json`. Keep the canonical full ID from `data.id`.\n")
	builder.WriteString("2. Claim it with `workbook update <id> --status in-progress --json` before\n")
	builder.WriteString("   editing files.\n")
	builder.WriteString("3. Move it to `in-review` once the change is ready for human review.\n")
	builder.WriteString("4. Move it to `done` only after the work is accepted and merged.\n\n")

	builder.WriteString("## Machine-readable output\n\n")
	builder.WriteString("Every command accepts `--json` except `serve`. Success is a single compact\n")
	builder.WriteString("line: `{\"format\":\"workbook.result\",\"version\":1,\"command\":...,\"data\":...}`.\n")
	builder.WriteString("Failure uses `\"format\":\"workbook.error\"` with an `error.category` field.\n")
	builder.WriteString("Check the result of every mutation; do not assume it succeeded.\n\n")

	builder.WriteString("## Exit codes\n\n")
	builder.WriteString("| Code | Category | What to do |\n| --- | --- | --- |\n")
	builder.WriteString("| 0 | success | nothing |\n")
	for _, category := range []struct {
		category core.Category
		remedy   string
	}{
		{core.CategoryOperational, "read the message; the environment or remote is at fault"},
		{core.CategoryInvocation, "fix the command line"},
		{core.CategoryNotInitialized, "run `workbook setup`"},
		{core.CategoryNotFound, "use an existing task ID"},
		{core.CategoryValidation, "change the input; it fails the same way on every retry"},
		{core.CategoryStaleWrite, "retry the identical command; it will probably succeed"},
		{core.CategoryCorruptData, "read the message; repair or rebuild before continuing"},
		{core.CategoryConflict, "read the envelope's `conflict` list, change the input, then retry"},
	} {
		builder.WriteString("| " + strconv.Itoa(core.ExitCode(core.Errorf(category.category, "x"))) +
			" | `" + string(category.category) + "` | " + category.remedy + " |\n")
	}
	builder.WriteString("\n")

	builder.WriteString("## Publication is automatic\n\n")
	builder.WriteString("Commands that create or update a task fetch shared task refs from `origin`,\n")
	builder.WriteString("apply the change to the refreshed tip, then publish the single ref they\n")
	builder.WriteString("changed. `workbook next` fetches before answering. A repository with no\n")
	builder.WriteString("`origin` synchronizes nothing.\n\n")
	builder.WriteString("Disable it for one command with `--no-sync`, for this project with\n")
	builder.WriteString("`workbook config set auto-sync false`, or for every project with\n")
	builder.WriteString("`\"autoSync\": false` in the user configuration's `preferences` block. A project\n")
	builder.WriteString("policy outranks a user preference; `--no-sync` outranks both.\n")
	builder.WriteString("`workbook config show` reports the resolved policy and which layer decided it.\n")
	builder.WriteString("Record a project policy with that command rather than editing\n")
	builder.WriteString("`.workbook/config.json`.\n\n")
	builder.WriteString("The `sync` member of a result envelope reports what happened. A `failed`\n")
	builder.WriteString("status still means the change was recorded locally and the command exits 0.\n")
	builder.WriteString("Local work that `origin` does not have is replayed onto the fetched tip and\n")
	builder.WriteString("published, so a divergent task needs no separate reconciliation step.\n\n")
	builder.WriteString("## Conflicts\n\n")
	builder.WriteString("Concurrent edits to different fields are applied silently. Exactly three\n")
	builder.WriteString("situations stop a replay and exit `8`: both sides changed the description, a\n")
	builder.WriteString("replayed dependency would close a cycle, and `origin` tombstoned a task a\n")
	builder.WriteString("local operation still edits.\n\n")
	builder.WriteString("They are reported in the result envelope's `conflict` list, which names each\n")
	builder.WriteString("task and a `type` of `description`, `dependency-cycle`, or `tombstone`. The\n")
	builder.WriteString("task ref stops at the last operation that replayed cleanly, everything up to\n")
	builder.WriteString("that point is published, and the remaining local operations are dropped.\n")
	builder.WriteString("Resolve one by reading the reported values and running the ordinary command\n")
	builder.WriteString("again; there is no reconcile or continue command. A conflict on one task\n")
	builder.WriteString("never blocks a command that touches a different task.\n\n")
	builder.WriteString("A running watcher does remember conflicts between invocations, because it\n")
	builder.WriteString("meets them with nobody present and a stopped replay leaves nothing for the\n")
	builder.WriteString("next fetch to find. It reports each one to its own terminal, gates the next\n")
	builder.WriteString("mutation of that task, and forgets it once reported or once the task moves\n")
	builder.WriteString("on, so the retry behaves exactly as it does without one.\n\n")
	builder.WriteString("`workbook fetch`, `workbook push`, and `workbook sync` remain available for\n")
	builder.WriteString("explicit whole-project synchronization.\n\n")
	builder.WriteString("## Continuous synchronization\n\n")
	builder.WriteString("`workbook sync --watch` runs in the foreground and keeps this clone current.\n")
	builder.WriteString("While one runs, a mutation writes locally, hands publication to it, and\n")
	builder.WriteString("reports a `sync` status of `deferred` instead of fetching and pushing\n")
	builder.WriteString("itself, which is roughly 500 ms and 16 Git processes cheaper. `workbook\n")
	builder.WriteString("serve` runs the same loop, so the board reflects other clones' work.\n\n")
	builder.WriteString("It is an optimization and never a requirement. With no watcher running, or\n")
	builder.WriteString("one that is stale or whose last synchronization failed, commands\n")
	builder.WriteString("synchronize inline exactly as before. `deferred` is best-effort: the local\n")
	builder.WriteString("write is durable and publication follows within milliseconds, but a watcher\n")
	builder.WriteString("killed in that window leaves the work local until `workbook push` runs.\n")
	builder.WriteString("`workbook sync --status` reports whether one is running and what it last\n")
	builder.WriteString("did.\n\n")

	builder.WriteString("---\n\n")
	builder.WriteString("This file is generated by Workbook. Edits are reported as local\n")
	builder.WriteString("modifications and preserved. Refresh it with `workbook docs update`, and\n")
	builder.WriteString("check it with `workbook docs status`.\n")

	return builder.String()
}

// RenderReference produces the managed block added to agent documentation
// files such as AGENTS.md and CLAUDE.md.
func RenderReference() string {
	return "## Workbook\n\n" +
		"This project tracks tasks with the Workbook CLI. Read\n" +
		"[`" + GuidelinesPath + "`](" + GuidelinesPath + ") for agent workflows and the\n" +
		"canonical machine values this project accepts, such as `in-progress` rather\n" +
		"than the `In Progress` display label.\n\n" +
		"Refresh this section with `workbook docs update`.\n"
}

// skillDocument builds the managed skill from the canonical definition,
// keeping its YAML frontmatter ahead of the managed block so that skill
// discovery still finds it on line one.
func skillDocument(generator string) (Document, error) {
	preamble, body, err := splitFrontmatter(skills.SkillMarkdown)
	if err != nil {
		return Document{}, err
	}
	return Document{Generator: generator, Preamble: preamble, Body: body}, nil
}

func splitFrontmatter(contents string) (string, string, error) {
	const delimiter = "---\n"
	if !strings.HasPrefix(contents, delimiter) {
		return "", "", core.Errorf(core.CategoryCorruptData, "embedded skill is missing YAML frontmatter")
	}
	rest := contents[len(delimiter):]
	end := strings.Index(rest, "\n"+delimiter)
	if end < 0 {
		return "", "", core.Errorf(core.CategoryCorruptData, "embedded skill has unterminated YAML frontmatter")
	}
	boundary := len(delimiter) + end + 1 + len(delimiter)
	return contents[:boundary], strings.TrimLeft(contents[boundary:], "\n"), nil
}
