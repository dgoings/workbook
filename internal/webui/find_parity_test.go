package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// findCase is one question put to both matchers: a task and a query.
type findCase struct {
	Title, Description, Query string
}

// The board's search and `list --find` must agree on every query, and the two
// implementations are in different languages. This slices the board's matcher
// out of the served script between its find:begin/find:end markers, runs it in
// Node over the same table Go runs, and compares every answer.
//
// The script comes from the handler rather than from assets/index.html, the way
// every other test on this page gets it. What a reader runs is what the handler
// served; a test that read the file on disk would keep passing if the handler
// ever stopped shipping this block, or shipped a rewritten one. The markers are
// string statements rather than comments for exactly that reason: html/template
// strips the comments out of the script it serves.
func TestBoardFindMatchesCoreMatcher(t *testing.T) {
	node := requireNode(t)
	source := boardPage(t)
	const begin = `"find:begin";`
	const finish = `"find:end";`
	start := strings.Index(source, begin)
	end := strings.Index(source, finish)
	if start < 0 || end <= start {
		t.Fatal("the served board no longer carries the find:begin/find:end markers")
	}
	matcher := source[start+len(begin) : end]

	cases := []findCase{
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
		// Greek final sigma. A browser lowercases "ΟΔΟΣ" to "οδος" because it can
		// see that the sigma ends a word; Go has no such context and answers
		// "οδοσ". Folding one code point at a time is what brings the board back
		// to Go's answer, so all three of these are a claim about that.
		{"ΟΔΟΣ", "", "οδοσ"},
		{"ΟΔΟΣ", "", "οδος"},
		{"ΟΔΟΣ", "", "ΟΔΟΣ"},
		// U+FEFF is whitespace to JavaScript's \s and not whitespace to Go's
		// unicode.IsSpace. The first row is one term to both matchers and finds
		// the task; the second is one term to Go, which does not find it, and was
		// two terms to the board, which did.
		{"alpha\ufeffbeta", "", "alpha\ufeffbeta"},
		{"alpha beta", "", "alpha\ufeffbeta"},
		// A query with the space taken out is a different term, and neither
		// matcher joins words across one.
		{"alpha beta", "", "alphabeta"},
		// U+0085 is the other direction: whitespace to Go and not to \s. Go reads
		// two terms and finds the task; the board read one and did not.
		{"alpha beta", "", "alpha\u0085beta"},
		// The two the matchers already agreed about, stated so that a narrowing of
		// the board's whitespace class would be caught as well as a widening.
		{"Audit the ledger", "", "audit\u00a0ledger"},
		{"Audit the ledger", "", "audit\u2028ledger"},
	}
	// The pairs the two matchers cannot agree on, asserted to still disagree.
	//
	// "İ" (U+0130) is lowercased by JavaScript's special casing to "i" followed by
	// a combining dot above, and by Go's simple case mapping to a bare "i".
	// Nothing the board can do per code point closes that, and closing it would
	// mean shipping a case-mapping table to the browser for one letter. It is
	// written down here rather than left out so that whoever does reconcile the
	// two — a Go change, a new JavaScript engine, a table on the page — is told
	// that this row is the reason the exclusion existed.
	knownDivergences := []findCase{
		{"İstanbul", "", "istanbul"},
	}

	all := append(append([]findCase{}, cases...), knownDivergences...)
	want := make([]bool, len(all))
	for i, c := range all {
		want[i] = core.MatchesFind(core.Task{TaskData: core.TaskData{Title: c.Title, Description: c.Description}}, c.Query)
	}
	table, _ := json.Marshal(all)
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
	if len(got) != len(all) {
		t.Fatalf("the board answered %d of %d cases", len(got), len(all))
	}
	for i, c := range cases {
		if got[i] != want[i] {
			t.Errorf("case %d %+v: board says %v, core says %v", i, c, got[i], want[i])
		}
	}
	for i, c := range knownDivergences {
		at := len(cases) + i
		if got[at] == want[at] {
			t.Errorf("documented divergence %+v now agrees (both say %v): the two matchers have been "+
				"reconciled, so move this row into the table above and drop the exclusion from the "+
				"find:begin comment", c, got[at])
		}
	}
}
