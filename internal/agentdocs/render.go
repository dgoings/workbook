package agentdocs

import (
	"fmt"
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
//
// The statuses come from the project's own vocabulary rather than from the
// built-in one, which is what makes these guidelines true for a project that
// renamed a column. The zero vocabulary means "this caller configured none" and
// renders core.LegacyVocabulary, exactly as core.Service reads it, so a project
// with no configuration ledger still gets the six statuses it is using rather
// than the five this build would mint a new project with.
func RenderGuidelines(project core.ProjectConfig, vocabulary core.Vocabulary) string {
	if vocabulary.IsZero() {
		vocabulary = core.LegacyVocabulary()
	}
	definitions := vocabulary.Definitions()
	var builder strings.Builder

	builder.WriteString("# Workbook guidelines\n\n")
	builder.WriteString("Workbook tracks this project's tasks in Git refs under `refs/workbook/tasks/`.\n")
	builder.WriteString("Use the `workbook` CLI as the task-state boundary. Never edit Workbook refs,\n")
	builder.WriteString("the SQLite projection, or `.workbook/config.json` directly.\n\n")

	builder.WriteString("## This project\n\n")
	builder.WriteString("| Setting | Value |\n| --- | --- |\n")
	builder.WriteString("| Project ID | `" + project.ProjectID + "` |\n")
	builder.WriteString("| Task ID prefix | `" + project.Key + "-` |\n\n")

	builder.WriteString("## Statuses\n\n")
	builder.WriteString("This project's statuses, in order. Pass the machine value, never the display\n")
	builder.WriteString("label.\n\n")
	builder.WriteString("| # | Machine value | Display label | Tags |\n| --- | --- | --- | --- |\n")
	for index, definition := range definitions {
		builder.WriteString("| " + strconv.Itoa(index+1) +
			" | `" + string(definition.Status) + "` | " + tableCell(definition.Label) +
			" | " + renderStatusTags(definition.Tags) + " |\n")
	}
	builder.WriteString("\n")
	builder.WriteString(displayLabelWarning(definitions))
	builder.WriteString("A display label is rejected as a validation error.\n\n")
	builder.WriteString("A tag is a job the machine gives a status, not a description of its name:\n\n")
	builder.WriteString("| Tag | What it makes Workbook do |\n| --- | --- |\n")
	// Driven by the tag set core defines, so a tag added there and left
	// undescribed here renders an empty cell the pinned test fails on.
	for _, tag := range core.StatusTags() {
		builder.WriteString("| `" + string(tag) + "` | " + statusTagMeaning(tag) + " |\n")
	}
	builder.WriteString("\nA status carrying no tag is an ordinary column: work rests there and nothing\n")
	builder.WriteString("else follows from it.\n\n")
	builder.WriteString("These statuses belong to this project and another project's are different, so\n")
	builder.WriteString("read them here or with `workbook status list --json` rather than assuming the\n")
	builder.WriteString("ones you have seen elsewhere. This section is rewritten whenever they change.\n\n")

	builder.WriteString("## Canonical priorities\n\n")
	builder.WriteString("| Machine value | Display label |\n| --- | --- |\n")
	for _, definition := range core.Priorities() {
		builder.WriteString("| `" + string(definition.Priority) + "` | " + definition.Label + " |\n")
	}
	builder.WriteString("\n")

	builder.WriteString("## Task lifecycle\n\n")
	// Every sentence here is read off the tags rather than off a status name,
	// because a project may call these columns anything. Markdown joins the
	// lines into one paragraph, which is what lets each sentence be composed
	// from a list whose length nobody knows in advance.
	builder.WriteString(defaultStatusSentence(vocabulary))
	builder.WriteString(taggedStatusSentence(definitions, core.StatusTagNext,
		"`workbook next` claims from %s.\n",
		"No status is tagged `next`, so `workbook next` never returns a task.\n"))
	builder.WriteString(taggedStatusSentence(definitions, core.StatusTagDone,
		"A dependency is satisfied once it reaches %s.\n",
		"No status is tagged `done`, so no dependency is ever satisfied.\n"))
	builder.WriteString("\n")
	builder.WriteString("1. Select work with `workbook next --json`, or read a known task with\n")
	builder.WriteString("   `workbook show <id> --json`. Keep the canonical full ID from `data.id`.\n")
	builder.WriteString("2. Claim it with `workbook update <id> --status <status> --json` before\n")
	builder.WriteString("   editing files, naming the status this project uses for work under way.\n")
	builder.WriteString("3. Move it along the statuses above as the work progresses, including the\n")
	builder.WriteString("   one this project uses for review, and into a status tagged `done` only\n")
	builder.WriteString("   after the work is accepted and merged.\n\n")

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

// authoredText renders a value somebody wrote for prose inside a generated
// file.
//
// A display label is written by whoever can push to the shared configuration
// ref, which makes it untrusted text. core.DisplayLine collapses the control
// runes and newlines, and the managed block's markers are neutralized here as
// well as in the block format itself: a label carrying the end marker would
// otherwise terminate the block early, and the file would read as somebody's
// local edit in every clone forever. Document.managedBody is the layer that
// makes that impossible for any body; this is the layer that keeps the
// guidelines' own untrusted values from ever reaching it.
func authoredText(value string) string {
	return neutralizeMarkers(core.DisplayLine(value))
}

// tableCell renders an authored value inside a Markdown table cell. A pipe is
// escaped because a pipe is what ends a cell: a label of `a | b` would
// otherwise silently add a column to the table and shift every value in the row.
func tableCell(value string) string {
	return strings.ReplaceAll(authoredText(value), "|", `\|`)
}

// codeSpan renders an authored value as a Markdown code span.
//
// The fence is as long as it has to be, which is CommonMark's own rule: a code
// span may contain a run of backticks shorter than its delimiter, and a value
// beginning or ending with one needs a space of padding to keep it. A label of
// `a` would otherwise close the span it was put in and leave the sentence
// around it rendered as code.
func codeSpan(value string) string {
	fence := "`"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	padding := ""
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		padding = " "
	}
	return fence + padding + value + padding + fence
}

// renderStatusTags renders a status's tag set for the table, saying "none"
// where there is nothing rather than leaving a cell a reader has to interpret.
// It is the word `workbook status list` prints for the same set, so the two
// surfaces read alike.
func renderStatusTags(tags []core.StatusTag) string {
	if len(tags) == 0 {
		return "none"
	}
	rendered := make([]string, 0, len(tags))
	for _, tag := range tags {
		rendered = append(rendered, "`"+string(tag)+"`")
	}
	return strings.Join(rendered, ", ")
}

// statusTagMeaning says what a tag makes Workbook do, phrased for the agent
// reading these guidelines rather than for the person configuring them.
//
// The switch is exhaustive over core.StatusTags by construction: the table
// above iterates that list, so a tag added to core and not described here
// renders an empty cell, which the pinned rendering test fails on.
func statusTagMeaning(tag core.StatusTag) string {
	switch tag {
	case core.StatusTagDefault:
		return "A task created without `--status` lands here. Exactly one status carries it."
	case core.StatusTagNext:
		return "`workbook next` may return a task sitting here."
	case core.StatusTagDone:
		return "A dependency sitting here is satisfied, so the work waiting on it can be claimed."
	default:
		return ""
	}
}

// displayLabelWarning shows the mistake this project's own labels invite.
//
// Multi-word labels are preferred as the example because they are where the
// mistake actually happens — nobody types `--status Backlog` as often as they
// type `--status In Progress` — and a project whose labels are all single words
// gets the first label that differs from its token instead. A project whose
// labels are exactly their tokens has no trap to warn about, so it gets no
// example.
func displayLabelWarning(definitions []core.StatusDefinition) string {
	var candidates []core.StatusDefinition
	for _, definition := range definitions {
		if strings.Contains(definition.Label, " ") {
			candidates = append(candidates, definition)
		}
	}
	if len(candidates) == 0 {
		for _, definition := range definitions {
			if definition.Label != string(definition.Status) {
				candidates = append(candidates, definition)
			}
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	warning := "Write `--status " + string(candidates[0].Status) + "`, not " +
		codeSpan(authoredText(candidates[0].Label)) + ".\n"
	if len(candidates) == 1 {
		return warning
	}
	rest := make([]string, 0, len(candidates)-1)
	for _, definition := range candidates[1:] {
		rest = append(rest, "`"+string(definition.Status)+"`")
	}
	return warning + "The same applies to " + joinPhrases(rest, "and") + ".\n"
}

// defaultStatusSentence says where a new task lands, or that nowhere does.
//
// The second case is reachable without anybody authoring it: a folded
// configuration can arrive from a peer with no default at all, and guidelines
// that claimed one would send an agent looking for a column that does not
// exist.
func defaultStatusSentence(vocabulary core.Vocabulary) string {
	status := vocabulary.Default()
	if status == "" {
		return "No status is tagged `default`, so a new task has nowhere to land.\n"
	}
	return "New tasks land in `" + string(status) + "`.\n"
}

// taggedStatusSentence renders one tag-derived sentence, naming every status
// that carries the tag, and says what is true instead when none does.
func taggedStatusSentence(
	definitions []core.StatusDefinition,
	tag core.StatusTag,
	sentence, none string,
) string {
	var names []string
	for _, definition := range definitions {
		if definition.HasTag(tag) {
			names = append(names, "`"+string(definition.Status)+"`")
		}
	}
	if len(names) == 0 {
		return none
	}
	return fmt.Sprintf(sentence, joinPhrases(names, "or"))
}

// joinPhrases joins rendered names into a readable list, with the serial comma
// the rest of this documentation uses.
func joinPhrases(names []string, conjunction string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " " + conjunction + " " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", " + conjunction + " " + names[len(names)-1]
	}
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
