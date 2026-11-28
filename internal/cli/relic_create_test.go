package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

func TestRelicCreateSetsProperties(t *testing.T) {
	t.Parallel()
	for _, objectFormat := range []string{testrepo.FormatSHA1, testrepo.FormatSHA256} {
		t.Run(objectFormat, func(t *testing.T) {
			t.Parallel()
			repository := relicRepositoryFor(t, objectFormat)

			// Each refusal writes nothing.
			message := relicErrorMessage(t, repository, 5, core.CategoryValidation,
				"create", "Compaction", "--type", "spec")
			if !strings.HasPrefix(message, "relic does not fit schema workbook:spec:1: ") ||
				!strings.Contains(message, "status") || !strings.HasSuffix(message, "(required)") {
				t.Errorf("create without --set = %q, want the built-in's refusal naming status", message)
			}
			document := builtInSpecFile(t)
			for _, option := range [][]string{{"--set", "status=draft"}, {"--set-json", `status="draft"`}, {"--set", "malformed"}} {
				message = relicErrorMessage(t, repository, 2, core.CategoryInvocation,
					append([]string{"create", "--from-file", document}, option...)...)
				if want := "relic create --from-file takes its properties from the document; leave out --set and --set-json"; message != want {
					t.Errorf("create --from-file with %s = %q, want %q", option, message, want)
				}
			}
			for _, test := range []struct {
				args     []string
				code     int
				category core.Category
				want     string
			}{
				{[]string{"--set", "status"}, 2, core.CategoryInvocation, "relic create --set takes <name>=<value>"},
				{[]string{"--set-json", "status"}, 2, core.CategoryInvocation, "relic create --set-json takes <name>=<json>"},
				{[]string{"--set", "status=draft", "--set", "status=approved"}, 2, core.CategoryInvocation,
					`relic create sets property "status" twice; name a property once`},
				{[]string{"--set", "status=draft", "--set-json", "status=1"}, 2, core.CategoryInvocation,
					`relic create sets property "status" twice; name a property once`},
				// A command line put together wrongly is refused before any
				// value on it is judged.
				{[]string{"--set-json", "owners=[", "--set", "status=a", "--set", "status=b"}, 2, core.CategoryInvocation,
					`relic create sets property "status" twice; name a property once`},
				{[]string{"--set", "status=draft", "--set-json", "owners=["}, 5, core.CategoryValidation, `--set-json "owners": `},
				{[]string{"--set", "status=wip"}, 5, core.CategoryValidation, "(enum)"},
			} {
				message = relicErrorMessage(t, repository, test.code, test.category,
					append([]string{"create", "Compaction", "--type", "spec"}, test.args...)...)
				if !strings.Contains(message, test.want) {
					t.Errorf("create %q = %q, want %q", test.args, message, test.want)
				}
			}
			if got := refsUnder(t, repository, "refs/workbook/relics/"); got != "" {
				t.Fatalf("a refused create wrote a relic:\n%s", got)
			}

			created := mustCreateRelic(t, repository, "Compaction", "--type", "spec",
				"--set", "status=draft", "--set-json", `owners=["sam"]`, "--set", "story=a=b")
			if created.Relic.RelicConformance == nil || created.Relic.State != core.RelicConformanceConforms ||
				created.Relic.Schema == nil || created.Relic.Schema.ID != "workbook:spec:1" {
				t.Fatalf("create = %s, want a conforming spec under workbook:spec:1", mustJSON(t, created))
			}
			var shown core.RelicDocument
			mustRelic(t, repository, &shown, "show", created.Relic.ID)
			properties, err := json.Marshal(shown.Properties)
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"owners":["sam"],"status":"draft","story":"a=b"}`; string(properties) != want {
				t.Errorf("properties = %s, want %s", properties, want)
			}
			if shown.State != core.RelicConformanceConforms {
				t.Errorf("show = %+v, want conforms", shown.RelicConformance)
			}

			// A type with no schema takes properties too.
			opened := mustCreateRelic(t, repository, "Notes", "--type", "note", "--set", "topic=sync")
			mustRelic(t, repository, &shown, "show", opened.Relic.ID)
			if string(shown.Properties["topic"]) != `"sync"` {
				t.Errorf("open relic's properties = %v, want topic sync", shown.Properties)
			}
		})
	}
}

func TestRelicCreateHelpNamesTheBuiltIns(t *testing.T) {
	t.Parallel()
	output := assertHelpOutput(t, []string{"relic", "create", "--help"},
		"Usage: workbook relic create [<title>] [--type <type>] [--set <name>=<value>]... [--set-json <name>=<json>]... [--from-file <file>] [--no-sync] [--json]\n")
	for _, want := range []string{"workbook:spec:1", "workbook:plan:1", "--set status=draft", "\n  --set ", "\n  --set-json "} {
		if !strings.Contains(output, want) {
			t.Errorf("relic create help = %q, want %q", output, want)
		}
	}
}
