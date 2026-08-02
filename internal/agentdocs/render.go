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
	builder.WriteString("| Code | Category |\n| --- | --- |\n")
	builder.WriteString("| 0 | success |\n")
	for _, category := range []core.Category{
		core.CategoryOperational,
		core.CategoryInvocation,
		core.CategoryNotInitialized,
		core.CategoryNotFound,
		core.CategoryValidation,
		core.CategoryStaleWrite,
		core.CategoryCorruptData,
	} {
		builder.WriteString("| " + strconv.Itoa(core.ExitCode(core.Errorf(category, "x"))) + " | `" + string(category) + "` |\n")
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
	builder.WriteString("Exit code 6 means the task diverged from `origin` and was not published;\n")
	builder.WriteString("reconcile with `workbook sync` rather than retrying the mutation.\n\n")
	builder.WriteString("`workbook fetch`, `workbook push`, and `workbook sync` remain available for\n")
	builder.WriteString("explicit whole-project synchronization.\n\n")

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
