package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
)

// recipeSection returns the guidelines' "Writing a spec or a plan" block, or
// "" when they have none.
func recipeSection(guidelines string) string {
	start := strings.Index(guidelines, "\n### Writing a spec or a plan\n")
	if start < 0 {
		return ""
	}
	section := guidelines[start+1:]
	if end := strings.Index(section, "\n##"); end >= 0 {
		section = section[:end+1]
	}
	return section
}

// shellBlocks returns the lines of each ```sh block in text, in order.
func shellBlocks(text string) [][]string {
	var blocks [][]string
	for {
		open := strings.Index(text, "```sh\n")
		if open < 0 {
			return blocks
		}
		text = text[open+len("```sh\n"):]
		end := strings.Index(text, "```")
		blocks = append(blocks, strings.Split(strings.TrimSuffix(text[:end], "\n"), "\n"))
		text = text[end+len("```"):]
	}
}

// shellWords splits a command line as a shell does for the quoting the
// guidelines use: spaces separate words, and single or double quotes keep
// what they hold in one word. Anything a shell would expand outside quotes is
// refused, so a line that passes means the same to the test and to a shell.
func shellWords(t *testing.T, line string) []string {
	t.Helper()
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	for _, character := range line {
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
				continue
			}
			word.WriteRune(character)
		case character == '"' || character == '\'':
			quote, inWord = character, true
		case character == ' ':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case strings.ContainsRune("*?[]$&|;()<>\\`", character):
			t.Fatalf("line %q has %q outside quotes", line, character)
		default:
			word.WriteRune(character)
			inWord = true
		}
	}
	if quote != 0 {
		t.Fatalf("line %q has an unclosed quote", line)
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}

// An agent given only the guidelines writes a spec and a plan that conform to
// the built-in schemas: each recipe runs as written, with its placeholders
// filled in by values that hold spaces, as real titles do.
func TestGuidelinesRecipeWritesConformingSpecAndPlan(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	task := cliCreateTask(t, repository, "Implement compaction")
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("Keep a bundle of what was replaced.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The file a fresh project is given, written again by the command that
	// refreshes it.
	if code, stdout, stderr := run(t, repository, "docs", "update"); code != 0 {
		t.Fatalf("docs update = (%d, %q, %q)", code, stdout, stderr)
	}
	blocks := shellBlocks(recipeSection(readProjectFile(t, repository, agentdocs.GuidelinesPath)))
	if len(blocks) != 2 {
		t.Fatalf("recipe blocks = %q, want one for spec and one for plan", blocks)
	}

	for index, recipe := range []struct {
		relicType, schema string
		parts             []string
	}{
		{"spec", "workbook:spec:1", []string{"section", "decision", "open-question"}},
		{"plan", "workbook:plan:1", []string{"section", "step"}},
	} {
		values := map[string]string{
			"<title>":    "Archive compaction",
			"<question>": "Which format should the archive use?",
			"<choice>":   "A Git bundle",
			"<task>":     task.ID,
			"<file.md>":  body,
		}
		var relicID string
		for _, line := range blocks[index] {
			words := shellWords(t, fillRecipeLine(line, values))
			if len(words) < 3 || words[0] != "workbook" || words[1] != "relic" || words[len(words)-1] != "--json" {
				t.Fatalf("recipe line %q is not a workbook relic command with --json", line)
			}
			code, stdout, stderr := run(t, repository, words[1:]...)
			if code != 0 {
				t.Fatalf("%q = (%d, %q, %q)", words, code, stdout, stderr)
			}
			result := assertJSONResult(t, stdout, "relic "+words[2])
			var changed core.RelicMutationResult
			if err := json.Unmarshal(result.Data, &changed); err != nil {
				t.Fatal(err)
			}
			relicID = changed.Relic.ID
			values["<relic>"] = relicID
			if strings.Contains(line, "--add-part") {
				var shown core.RelicDocument
				mustRelic(t, repository, &shown, "show", relicID)
				values["<part>"] = shown.Parts[len(shown.Parts)-1].ID
			}
		}

		var shown core.RelicDocument
		mustRelic(t, repository, &shown, "show", relicID)
		if shown.Type != recipe.relicType || shown.Schema == nil || shown.Schema.ID != recipe.schema {
			t.Fatalf("%s recipe wrote type %q under %+v, want %s", recipe.relicType, shown.Type, shown.Schema, recipe.schema)
		}
		if shown.State != core.RelicConformanceConforms {
			t.Fatalf("%s conformance = %+v, want conforms", recipe.relicType, shown.RelicConformance)
		}
		var types []string
		for _, part := range shown.Parts {
			types = append(types, part.Type)
		}
		if strings.Join(types, " ") != strings.Join(recipe.parts, " ") {
			t.Fatalf("%s parts = %q, want %q", recipe.relicType, types, recipe.parts)
		}
		if len(shown.Links) != 1 || shown.Links[0].Rel != "implemented-by" {
			t.Fatalf("%s links = %s, want one implemented-by link", recipe.relicType, mustJSON(t, shown.Links))
		}
	}
}

// fillRecipeLine fills a recipe line's placeholders.
func fillRecipeLine(line string, values map[string]string) string {
	for placeholder, value := range values {
		line = strings.ReplaceAll(line, placeholder, value)
	}
	return line
}

// A project's own spec schema ends the spec recipe, which was written for the
// built-in, and leaves the plan's.
func TestGuidelinesAfterTakeover(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	mustPutSchema(t, repository, "spec", specSchemaOne)

	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	recipe := recipeSection(guidelines)
	if strings.Contains(recipe, "--type spec") || !strings.Contains(recipe, "--type plan") {
		t.Errorf("recipes after a spec takeover = %q, want the plan's only", recipe)
	}
	for _, want := range []string{"- `spec`, version 1\n", "- `plan`, built-in `workbook:plan:1`\n"} {
		if !strings.Contains(guidelines, want) {
			t.Errorf("guidelines after a spec takeover are missing %q", want)
		}
	}
	if strings.Contains(guidelines, "workbook:spec:1") {
		t.Errorf("guidelines still name the spec built-in after a takeover:\n%s", guidelines)
	}

	mustPutSchema(t, repository, "plan", specSchemaOne)
	if recipe := recipeSection(readProjectFile(t, repository, agentdocs.GuidelinesPath)); recipe != "" {
		t.Errorf("recipes after both takeovers = %q, want none", recipe)
	}
}

// The command scanner finds nothing unbuilt in what a project with the
// built-in schemas is given.
func TestGuidanceScannerAcceptsTheRecipes(t *testing.T) {
	t.Parallel()
	repository := initializedRepository(t)
	if code, stdout, stderr := run(t, repository, "docs", "update"); code != 0 {
		t.Fatalf("docs update = (%d, %q, %q)", code, stdout, stderr)
	}
	guidelines := readProjectFile(t, repository, agentdocs.GuidelinesPath)
	if len(shellBlocks(recipeSection(guidelines))) != 2 {
		t.Fatalf("guidelines have no recipes to scan:\n%s", guidelines)
	}
	for _, violation := range guidanceCommandViolations(guidelines) {
		t.Errorf("guidelines: %s", violation)
	}
}
