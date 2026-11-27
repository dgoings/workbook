package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/skills"
)

// guidanceCommandViolations checks every `workbook ...` invocation in the
// code of a Markdown text against the command schemas: each inline code span
// and each line of a fenced block. An invocation must name a command that
// exists, and a subcommand when its command has them; every --option given in
// the same span or line must be one that command takes. `help` and --help are
// always allowed. Prose outside code is not checked.
//
// Each violation names the line the code starts on and the invocation.
func guidanceCommandViolations(text string) []string {
	var violations []string
	for _, unit := range guidanceCodeUnits(text) {
		for _, invocation := range guidanceInvocations(unit.code) {
			if reason := guidanceInvocationViolation(invocation); reason != "" {
				violations = append(violations, fmt.Sprintf("line %d: `%s`: %s", unit.line, strings.Join(invocation, " "), reason))
			}
		}
	}
	return violations
}

// guidanceCode is one piece of code in a Markdown text and the line it starts on.
type guidanceCode struct {
	line int
	code string
}

// guidanceCodeUnits returns each line of every fenced block and every inline
// code span outside them. A span may run over several lines, as Markdown
// allows within a paragraph; a run of backticks with no closing run before the
// paragraph ends is literal text.
func guidanceCodeUnits(text string) []guidanceCode {
	var units []guidanceCode
	lines := strings.Split(text, "\n")
	var prose strings.Builder
	proseStart := 1
	flush := func() {
		units = append(units, guidanceCodeSpans(prose.String(), proseStart)...)
		prose.Reset()
	}
	fence := ""
	for index, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
				proseStart = index + 2
				continue
			}
			units = append(units, guidanceCode{line: index + 1, code: line})
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			marker := trimmed[:1]
			fence = strings.Repeat(marker, len(trimmed)-len(strings.TrimLeft(trimmed, marker)))
			continue
		}
		if prose.Len() == 0 {
			proseStart = index + 1
		}
		prose.WriteString(line)
		prose.WriteString("\n")
	}
	flush()
	return units
}

// guidanceCodeSpans returns the inline code spans of prose that starts on
// line first.
func guidanceCodeSpans(prose string, first int) []guidanceCode {
	var spans []guidanceCode
	for offset := 0; offset < len(prose); {
		if prose[offset] != '`' {
			offset++
			continue
		}
		run := len(prose[offset:]) - len(strings.TrimLeft(prose[offset:], "`"))
		opener := prose[offset : offset+run]
		rest := prose[offset+run:]
		end := -1
		for search := 0; search < len(rest); {
			found := strings.Index(rest[search:], opener)
			if found < 0 {
				break
			}
			found += search
			after := len(rest[found:]) - len(strings.TrimLeft(rest[found:], "`"))
			if after == run {
				end = found
				break
			}
			search = found + after
		}
		if end < 0 || strings.Contains(rest[:end], "\n\n") {
			offset += run
			continue
		}
		spans = append(spans, guidanceCode{
			line: first + strings.Count(prose[:offset], "\n"),
			code: strings.ReplaceAll(rest[:end], "\n", " "),
		})
		offset += run + end + run
	}
	return spans
}

// guidanceInvocations splits code into the `workbook` invocations it holds,
// each the words after `workbook` up to the end of the code or a shell
// operator.
func guidanceInvocations(code string) [][]string {
	var invocations [][]string
	var current []string
	inside := false
	for _, word := range strings.Fields(code) {
		switch {
		case guidanceShellOperator(word):
			if inside {
				invocations = append(invocations, current)
			}
			inside, current = false, nil
		case strings.TrimLeft(word, `"'$(`) == "workbook":
			if inside {
				invocations = append(invocations, current)
			}
			inside, current = true, []string{}
		case inside:
			current = append(current, word)
		}
	}
	if inside {
		invocations = append(invocations, current)
	}
	return invocations
}

func guidanceShellOperator(word string) bool {
	switch word {
	case "|", "||", "&&", ";", ">", ">>", "<", "2>", "2>&1":
		return true
	}
	return false
}

var guidanceOptionName = regexp.MustCompile(`^--([a-z0-9][a-z0-9-]*)`)

// guidanceInvocationViolation says what is wrong with one invocation, the
// words after `workbook`, or returns "" when nothing is.
func guidanceInvocationViolation(words []string) string {
	for _, word := range words {
		if word == "--help" || word == "-h" {
			return ""
		}
	}
	// The command path is the longest run of leading words the schemas know.
	var path []string
	var metadata commandMetadata
	rest := words
	for len(rest) > 0 {
		word := rest[0]
		if strings.HasPrefix(word, "-") || strings.HasPrefix(word, "<") || strings.HasPrefix(word, "[") {
			break
		}
		next, exists := commandMetadataFor(append(append([]string{}, path...), word))
		if !exists {
			switch {
			case len(path) == 0 && word == "help":
				return ""
			case len(path) == 0:
				return fmt.Sprintf("there is no command %q", word)
			case len(metadata.Subcommands) > 0:
				return fmt.Sprintf("%q has no subcommand %q", strings.Join(path, " "), word)
			}
			break
		}
		path, metadata, rest = append(path, word), next, rest[1:]
		if len(metadata.Subcommands) == 0 {
			break
		}
	}
	if len(path) > 0 && len(metadata.Subcommands) > 0 {
		return fmt.Sprintf("%q needs one of its subcommands: %s", strings.Join(path, " "), strings.Join(metadata.SubcommandOrder, ", "))
	}
	for _, word := range rest {
		match := guidanceOptionName.FindStringSubmatch(word)
		if match == nil || match[1] == "help" {
			continue
		}
		if len(path) == 0 {
			return fmt.Sprintf("--%s names no command", match[1])
		}
		if _, exists := metadata.optionKind(match[1]); !exists {
			return fmt.Sprintf("%q takes no --%s", strings.Join(path, " "), match[1])
		}
	}
	return ""
}

func TestGuidanceScannerReportsUnbuiltCommands(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"Compare with `workbook relic frobnicate <relic>`.",
		"Read it with `workbook relic show <relic> --made-up`.",
		"Every relic command is `workbook relic`.",
		"List them with `workbook widget list`.",
		"```sh\nworkbook relic show <relic>#<part> --pointer <path> --json\n```",
		"Run `workbook relic update <relic>\n--part <part> --body-file <file> --made-up --json`.",
		"```sh\ngit cat-file -p HEAD | workbook relic create --from-file - --template\n```",
	} {
		if violations := guidanceCommandViolations(text); len(violations) != 1 {
			t.Errorf("guidanceCommandViolations(%q) = %q, want one violation", text, violations)
		}
	}

	guidelines := agentdocs.RenderGuidelines(core.ProjectConfig{ProjectID: "01KY8964C8TQVBKVACB45DYTNY", Key: "WB"},
		core.Vocabulary{}, core.PriorityVocabulary{}, core.KeySet{}, nil)
	block := relicGuidanceBlock(t, guidelines)
	if len(block) != 4 {
		t.Fatalf("relic guidance block = %q, want four lines", block)
	}
	if violations := guidanceCommandViolations("```sh\n" + strings.Join(block, "\n") + "\n```\n"); len(violations) != 0 {
		t.Errorf("the relic guidance block has violations: %q", violations)
	}
	for _, text := range []string{
		"Use the `workbook` CLI, `workbook help relic` or `workbook relic --help`.",
		"Run `workbook update <id> --status <status> --assign self\n--json` first.",
		"`refs/workbook/relics/` and `.workbook/config.json` are not commands.",
	} {
		if violations := guidanceCommandViolations(text); len(violations) != 0 {
			t.Errorf("guidanceCommandViolations(%q) = %q, want none", text, violations)
		}
	}
	if got := guidanceCommandViolations("one\n\ntwo `workbook relic frobnicate`"); len(got) != 1 || !strings.HasPrefix(got[0], "line 3: `relic frobnicate`") {
		t.Errorf("violation = %q, want it to name line 3 and the invocation", got)
	}
}

func TestAgentGuidanceUsesOnlyImplementedCommandsAndOptions(t *testing.T) {
	t.Parallel()
	for name, text := range map[string]string{
		"guidelines": agentdocs.RenderGuidelines(core.ProjectConfig{ProjectID: "01KY8964C8TQVBKVACB45DYTNY", Key: "WB"},
			core.DefaultVocabulary(), core.PriorityVocabulary{}, core.KeySet{}, nil),
		"skill":     skills.SkillMarkdown,
		"reference": agentdocs.RenderReference(),
	} {
		for _, violation := range guidanceCommandViolations(text) {
			t.Errorf("%s: %s", name, violation)
		}
	}
}

// relicGuidanceBlock returns the lines of the fenced block in the guidelines'
// Relics section.
func relicGuidanceBlock(t *testing.T, guidelines string) []string {
	t.Helper()
	start := strings.Index(guidelines, "\n## Relics\n")
	if start < 0 {
		t.Fatalf("guidelines have no Relics section:\n%s", guidelines)
	}
	section := guidelines[start:]
	open := strings.Index(section, "```sh\n")
	if open < 0 {
		t.Fatalf("Relics section has no sh block:\n%s", section)
	}
	block := section[open+len("```sh\n"):]
	return strings.Split(strings.TrimSuffix(block[:strings.Index(block, "```")], "\n"), "\n")
}

// jsonKeys collects every object member name in a JSON value, at any depth.
func jsonKeys(t *testing.T, value any, into map[string]bool) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				into[key] = true
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decoded)
}

func TestGuidelinesNameRealRelicJSONMembers(t *testing.T) {
	t.Parallel()
	text, version := "one\n", "01M87F0T8ZACFWH3F27EK4CYVH"
	notText := "AP8="
	keys := map[string]bool{}
	jsonKeys(t, ResultEnvelope{
		Format: "workbook.result", Version: 1, Command: "sync",
		RelicConflict: []core.RelicConflict{{
			RelicID: "WB-R-01M87F0SQYHP3HCR4QEN3RTVEX", Type: "body", Part: version,
			Body: &core.RelicBodyConflict{
				Base:   &core.RelicBodyVersion{Version: version, Bytes: 4, Object: "a", Text: &text},
				Ours:   core.RelicBodyVersion{Version: version, Bytes: 4, Object: "b", Text: &text},
				Theirs: core.RelicBodyVersion{Version: version, Bytes: 2, Object: "c", Base64: &notText},
			},
			ParkedRef: "refs/workbook/reconciled-relics/WB-R-01M87F0SQYHP3HCR4QEN3RTVEX/1",
			Unapplied: []core.RelicUnappliedOperation{{OperationID: version, Operation: "title.set", Commit: "d"}},
		}},
	}, keys)
	jsonKeys(t, ResultEnvelope{Format: "workbook.result", Version: 1, Command: "relic show", Data: core.RelicDocument{
		Format: core.RelicDocumentFormat, Version: 1,
		Parts: []core.RelicDocumentPart{{
			ID: version, Type: "section",
			Body: &core.RelicDocumentBody{Version: version, Bytes: 4, Text: &text},
		}},
	}}, keys)
	jsonKeys(t, core.RelicDocumentBody{Version: version, Bytes: 2, Base64: &notText}, keys)
	jsonKeys(t, ResultEnvelope{Format: "workbook.result", Version: 1, Command: "show", Data: core.TaskDetail{
		Relics: []core.TaskRelicLink{{Relic: "WB-R-01M87F0SQYHP3HCR4QEN3RTVEX", Part: version, Link: version, Rel: "implemented-by"}},
	}}, keys)

	guidelines := agentdocs.RenderGuidelines(core.ProjectConfig{ProjectID: "01KY8964C8TQVBKVACB45DYTNY", Key: "WB"},
		core.Vocabulary{}, core.PriorityVocabulary{}, core.KeySet{}, nil)
	start := strings.Index(guidelines, "\n## Relics\n")
	if start < 0 {
		t.Fatalf("guidelines have no Relics section:\n%s", guidelines)
	}
	section := guidelines[start:]
	if end := strings.Index(section[1:], "\n## "); end >= 0 {
		section = section[:end+1]
	}
	for _, member := range []string{
		"relics", "relic", "part", "rel", "parts", "body", "text", "base64", "version",
		"relicConflict", "type", "base", "ours", "theirs", "parkedRef", "unapplied",
	} {
		if !keys[member] {
			t.Errorf("no JSON result has a member %q", member)
		}
		if !regexp.MustCompile("[`.]" + member + "[`.\\[]").MatchString(section) {
			t.Errorf("the Relics section does not name the member %q", member)
		}
	}
	for _, path := range []string{"data.relics", "data.parts[0].body.text", "data.parts[0].body.base64", "data.parts[0].body.version"} {
		if !strings.Contains(section, "`"+path+"`") {
			t.Errorf("the Relics section does not name %q", path)
		}
	}
}

// The guidelines' own command block, run in order with its placeholders filled
// in, finds the relic linked to a task, reads one part, and saves an edit to
// it; the same save from the version it replaced is then refused.
func TestGuidelinesRelicWorkflowRunsAsWritten(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Implement compaction")
	relicID := mustCreateRelic(t, repository, "Archive plan", "--type", "spec").Relic.ID
	one := filepath.Join(t.TempDir(), "one.md")
	if err := os.WriteFile(one, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added := mustUpdateRelic(t, repository, relicID, "--add-part", "--part-type", "section", "--body-file", one)
	partID := added.Parts[0].ID
	mustUpdateRelic(t, repository, relicID, "--link", task.ID, "--from", partID, "--rel", "implemented-by")
	two := filepath.Join(t.TempDir(), "two.md")
	if err := os.WriteFile(two, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	guidelines := agentdocs.RenderGuidelines(core.ProjectConfig{ProjectID: "01KY8964C8TQVBKVACB45DYTNY", Key: "WB"},
		core.Vocabulary{}, core.PriorityVocabulary{}, core.KeySet{}, nil)
	block := relicGuidanceBlock(t, guidelines)
	if len(block) != 4 {
		t.Fatalf("relic guidance block = %q, want four lines", block)
	}
	values := map[string]string{"<task>": task.ID, "<relic>": relicID, "<part>": partID, "<file>": two}
	command := func(line string) []string {
		words := strings.Fields(line)
		if words[0] != "workbook" {
			t.Fatalf("guidance line %q does not start with workbook", line)
		}
		args := make([]string, 0, len(words)-1)
		for _, word := range words[1:] {
			for placeholder, value := range values {
				word = strings.ReplaceAll(word, placeholder, value)
			}
			if strings.Contains(word, "<") {
				t.Fatalf("guidance line %q has a placeholder left in %q", line, word)
			}
			args = append(args, word)
		}
		return args
	}
	mustSucceed := func(line string) resultDocument {
		t.Helper()
		args := command(line)
		code, stdout, stderr := run(t, repository, args...)
		if code != 0 {
			t.Fatalf("%v = (%d, %q, %q)", args, code, stdout, stderr)
		}
		return assertJSONResult(t, stdout, strings.Join(args[:guidanceCommandWords(args)], " "))
	}
	readPart := func() core.RelicDocumentBody {
		t.Helper()
		var shown core.RelicDocument
		if err := json.Unmarshal(mustSucceed(block[2]).Data, &shown); err != nil {
			t.Fatal(err)
		}
		if len(shown.Parts) != 1 || shown.Parts[0].Body == nil || shown.Parts[0].Body.Text == nil {
			t.Fatalf("relic show part = %s", mustJSON(t, shown))
		}
		return *shown.Parts[0].Body
	}

	var detail core.TaskDetail
	if err := json.Unmarshal(mustSucceed(block[0]).Data, &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Relics) != 1 || detail.Relics[0].Relic != relicID || detail.Relics[0].Part != partID || detail.Relics[0].Rel != "implemented-by" {
		t.Fatalf("show data.relics = %s, want the link from %s#%s", mustJSON(t, detail.Relics), relicID, partID)
	}
	var listed []core.RelicSummary
	if err := json.Unmarshal(mustSucceed(block[1]).Data, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != relicID {
		t.Fatalf("relic list --linked-to = %s, want %s", mustJSON(t, listed), relicID)
	}
	first := readPart()
	if *first.Text != "one\n" {
		t.Fatalf("body = %q, want %q", *first.Text, "one\n")
	}
	values["<version>"] = first.Version
	mustSucceed(block[3])
	if second := readPart(); *second.Text != "two\n" || second.Version == first.Version {
		t.Fatalf("body after the save = %q at %s, want %q at a new version", *second.Text, second.Version, "two\n")
	}

	// The identical save again starts from the version it replaced.
	args := command(block[3])
	code, stdout, stderr := run(t, repository, args...)
	if code != 6 || stdout != "" {
		t.Fatalf("%v again = (%d, %q, %q), want exit 6", args, code, stdout, stderr)
	}
	assertJSONError(t, stderr, core.CategoryStaleWrite, "")
	if body := readPart(); *body.Text != "two\n" {
		t.Fatalf("body after the refused save = %q, want %q", *body.Text, "two\n")
	}
}

// guidanceCommandWords counts the leading words of args that name the command,
// as the result envelope's command member spells it.
func guidanceCommandWords(args []string) int {
	if args[0] == "relic" {
		return 2
	}
	return 1
}
