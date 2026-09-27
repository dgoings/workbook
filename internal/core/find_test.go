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
		if got := MatchesFind(task, tc.query); got != tc.want {
			t.Fatalf("MatchesFind(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}
