package webui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// The board's search and `list --find` must agree on every query, and the two
// implementations are in different languages. This slices the board's matcher
// out of the served script between its find:begin/find:end markers, runs it in
// Node over the same table Go runs, and compares every answer.
func TestBoardFindMatchesCoreMatcher(t *testing.T) {
	node := requireNode(t)
	page, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(page)
	start := strings.Index(source, "// find:begin")
	end := strings.Index(source, "// find:end")
	if start < 0 || end <= start {
		t.Fatal("the board script no longer carries the find:begin/find:end markers")
	}
	matcher := source[start:end]

	cases := []struct {
		Title, Description, Query string
	}{
		{"Audit card abilities", "resolve correctly", ""},
		{"Audit card abilities", "resolve correctly", "audit"},
		{"Audit card abilities", "resolve correctly", "AUDIT ABILITIES"},
		{"Audit card abilities", "resolve correctly", "abilities resolve"},
		{"Audit card abilities", "resolve correctly", "abilitiesresolve"},
		{"Audit card abilities", "resolve correctly", "  card\tabilities\n"},
		{"Audit card abilities", "resolve correctly", "audit missing"},
		{"Été à Paris", "Deuxième ligne", "été"},
		{"Été à Paris", "Deuxième ligne", "ÉTÉ LIGNE"},
		{"Привет мир", "", "привет"},
		{"punctuation, here!", "(parens) and [brackets]", "here! (parens)"},
		{"", "", "anything"},
		{"", "", ""},
	}
	want := make([]bool, len(cases))
	for i, c := range cases {
		want[i] = core.MatchesFind(core.Task{TaskData: core.TaskData{Title: c.Title, Description: c.Description}}, c.Query)
	}
	table, _ := json.Marshal(cases)
	program := matcher + `
const cases = ` + string(table) + `;
process.stdout.write(JSON.stringify(cases.map((c) => matchesFind({ title: c.Title, description: c.Description }, c.Query))));
`
	output, err := nodeCommand(node, program).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, output)
	}
	var got []bool
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode node answers: %v\n%s", err, output)
	}
	for i := range cases {
		if got[i] != want[i] {
			t.Errorf("case %d %+v: board says %v, core says %v", i, cases[i], got[i], want[i])
		}
	}
}
