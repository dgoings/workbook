package core

import "strings"

// FindTerms splits a search query the way both boards and the CLI agree to:
// case-folded, on whitespace, empty terms dropped. It is exported because the
// web board reimplements the same rule in JavaScript and a parity test holds
// the two together.
func FindTerms(query string) []string {
	return strings.Fields(strings.ToLower(query))
}

// MatchesFind reports whether every term of query appears in the task's title
// or description. Labels have a filter of their own and comments are not
// searched, so those are deliberately absent. Title and description are joined
// with a newline so a term cannot straddle the two. An empty query matches.
func MatchesFind(task Task, query string) bool {
	terms := FindTerms(query)
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(task.Title + "\n" + task.Description)
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}
