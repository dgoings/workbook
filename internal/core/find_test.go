package core

import "testing"

func TestFindTermsSplitsOnWhitespaceAndFolds(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"   ", nil},
		{"Perf", []string{"perf"}},
		{"  Battle\tBot\nfeature ", []string{"battle", "bot", "feature"}},
		{"ÉTÉ", []string{"été"}},
	} {
		got := FindTerms(tc.query)
		if len(got) != len(tc.want) {
			t.Fatalf("FindTerms(%q) = %v, want %v", tc.query, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("FindTerms(%q) = %v, want %v", tc.query, got, tc.want)
			}
		}
	}
}

func TestMatchesFindSearchesTitleAndDescriptionOnly(t *testing.T) {
	task := Task{
		TaskData: TaskData{
			Title:       "Audit the card abilities",
			Description: "Make sure every ability resolves correctly.\nSecond line.",
			Labels:      []string{"games", "urgent-label"},
			Comments:    []Comment{{Body: "hidden-in-comment"}},
		},
	}
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"", true},
		{"audit", true},
		{"AUDIT abilities", true},
		{"resolves correctly", true},
		{"abilities resolves", true}, // one term in the title, one in the description
		{"line. second", true},       // order does not matter
		{"audit missing", false},     // every term must match
		{"games", false},             // labels are not searched
		{"hidden-in-comment", false}, // comments are not searched
		{"abilitiesmake", false},     // title and description never run together
	} {
		if got := MatchesFind(task, tc.query, []string{"WB"}); got != tc.want {
			t.Fatalf("MatchesFind(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

// Pasting a task's ID into a search finds that one task. An ID is matched from
// its beginning only — a key, a dash, and more characters — which is what keeps
// a word that happens to occur inside a ULID from matching any of the tasks the
// reader was not asking about.
func TestMatchesFindMatchesATaskIDByPrefix(t *testing.T) {
	keys := []string{"WB", "ZZ"}
	task := Task{
		ID: "WB-01M2JEC8CBKB5S0THJNSF15WMV",
		TaskData: TaskData{
			Title:       "Notarize macOS builds",
			Description: "Staple the ticket.",
		},
	}
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"WB-01M2JEC8", true},                     // the start of the ID
		{"wb-01m2jec8", true},                     // a pasted ID need not be upper-cased
		{"WB-01M2JEC8CBKB5S0THJNSF15WMV", true},   // the whole ID
		{"WB-01M2JEC8CBKB5S0THJNSF15WMVX", false}, // longer than the ID it would name
		{"WB", false},                             // a key alone is a word, and this text has none
		{"WB-", false},                            // nor is a key and a dash a prefix
		{"ZZ-01M2", false},                        // another key's prefix is another key's tasks
		{"01M2JEC8", false},                       // a ULID without a key names no ID
		{"JEC8CBKB", false},                       // and never from the middle of one
		{"WB-01M2 notarize", true},                // one term by ID, one by title
		{"WB-01M2 missing", false},                // every term still has to match
		{"WB-01M2JEC8 WB-01M2JEC8CBKB", true},     // both by ID
		{"notarize wb-01m2jec8cbkb5s0thjnsf15wmv", true},
		// A term carrying anything outside ASCII is no ID prefix, whatever it
		// would fold to. JavaScript upper-cases "ß" to "SS" and Go leaves it, so a
		// term the two fold differently is refused before either folds it.
		{"wb-01m2ßkb", false},
		{"wb-01m2ſ", false},
	} {
		if got := MatchesFind(task, tc.query, keys); got != tc.want {
			t.Fatalf("MatchesFind(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
	// The text route is still open to a term that looks like a prefix: this task
	// has no such ID, and its title says the words.
	planning := Task{ID: "WB-01M2JEC8CBKB5S0THJNSF15WMW", TaskData: TaskData{Title: "WB-99 planning"}}
	if !MatchesFind(planning, "WB-99", []string{"WB"}) {
		t.Fatal(`MatchesFind("WB-99") = false, want true by text: the prefix route fails and the title holds the words`)
	}
	// With no keys at all nothing is a prefix term, so every term is text.
	if MatchesFind(task, "WB-01M2JEC8", nil) {
		t.Fatal(`MatchesFind("WB-01M2JEC8", no keys) = true, want false: without a key set nothing names an ID`)
	}
	// The task the folding difference actually reached: a term carrying "ß"
	// upper-cases to "SS" in a browser and stays "ß" in Go, so this ID was found
	// by the board and not by the CLI. Refused in both now, which is the answer
	// the two can agree on without a case table in the browser.
	expanding := Task{ID: "WB-01M2SSKB5S0THJNSF15WMVQ7", TaskData: TaskData{
		Title:       "Notarize macOS builds",
		Description: "Staple the ticket.",
	}}
	if MatchesFind(expanding, "wb-01m2ßkb", keys) {
		t.Fatal(`MatchesFind("wb-01m2ßkb") = true, want false: a term the two languages fold differently names no ID`)
	}
}

func TestFindKeyPrefixTermNamesTheStartOfAnID(t *testing.T) {
	keys := []string{"WB", "ZZ"}
	for _, tc := range []struct {
		term   string
		want   string
		wantOK bool
	}{
		{"WB-01M2", "WB-01M2", true},
		{"wb-01m2", "WB-01M2", true}, // folded to the case an ID is written in
		{"ZZ-0", "ZZ-0", true},       // one character past the dash is enough
		{"WB", "", false},
		{"WB-", "", false},
		{"XX-01M2", "", false},   // no such key
		{"WB-01M2!", "", false},  // an ID holds only letters and digits
		{"WB-01M2 ", "", false},  // including no space
		{"WB-01M2-3", "", false}, // and no second dash
		{"", "", false},
		// Checked before folding: only ASCII letters, digits and dashes reach the
		// upper-casing, because that is the one operation the board and Go cannot
		// be made to agree about for anything else.
		{"wb-01m2ßkb", "", false},
		{"wb-01m2ſ", "", false},
		{"WB-01M2É", "", false},
		// The same two refusals under the second key, so that the loop is known to
		// reach it. Two dash-free keys can never both be the prefix of one term,
		// so nothing here can tell a key whose alphabet check failed being skipped
		// from the whole term being given up on; the loop moves to the next key
		// anyway, because that is what its sentence says it does.
		{"ZZ-01M2-3", "", false},
		{"ZZ-01M2ß", "", false},
	} {
		got, ok := FindKeyPrefixTerm(tc.term, keys)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("FindKeyPrefixTerm(%q) = %q, %v, want %q, %v", tc.term, got, ok, tc.want, tc.wantOK)
		}
	}
}
