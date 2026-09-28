package core

import "strings"

// FindTerms splits a search query the way both boards and the CLI agree to:
// case-folded, on whitespace, empty terms dropped. It is exported because the
// web board reimplements the same rule in JavaScript and a parity test holds
// the two together.
func FindTerms(query string) []string {
	return strings.Fields(strings.ToLower(query))
}

// FindKeyPrefixTerm reports whether term names the start of a task ID under one
// of keys: the key, a dash, and at least one more character, compared without
// regard to case. It answers the term folded to upper case, which is the form a
// task ID is written in and so the form to compare against.
//
// "WB" and "WB-" are words rather than prefixes: a reader who types either is
// far likelier to be looking for those letters in some title than for every
// task the project has ever minted. And an ID is matched only from its
// beginning, which is the whole reason this question is asked — a word that
// happens to occur inside a ULID must never drag in a card nobody was looking
// for.
//
// The term is checked before it is folded, and only ASCII letters, digits and
// dashes pass. That is what keeps this answer the same in both languages: upper
// case is not a per-character operation, and JavaScript expands where Go does
// not — "ß".toUpperCase() is "SS" and strings.ToUpper leaves it alone, so
// "wb-01m2ßkb" named the ID WB-01M2SSKB… on the board and named nothing to the
// CLI. Refusing the term outright rather than folding it is the one fix that
// needs no case table in the browser: with nothing outside ASCII left there is
// nothing the two can fold differently. "wb-01m2ſ" stops naming an ID in both
// for the same reason, deliberately — an ID-prefix term is typed in the ID's own
// alphabet, and a term that is not still has the whole text route open to it.
func FindKeyPrefixTerm(term string, keys []string) (string, bool) {
	for index := 0; index < len(term); index++ {
		character := term[index]
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-':
		default:
			return "", false
		}
	}
	upper := strings.ToUpper(term)
	for _, key := range keys {
		if len(upper) <= len(key)+1 || !strings.HasPrefix(upper, key+"-") {
			continue
		}
		// Only a second dash can fail here, now that the term is ASCII; the
		// check is written as the alphabet it is so that it stays true of the
		// rule rather than of what the validation above happens to leave.
		rest := upper[len(key)+1:]
		alphabet := true
		for _, r := range rest {
			if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				alphabet = false
				break
			}
		}
		if alphabet {
			return upper, true
		}
	}
	return "", false
}

// MatchesFind reports whether every term of query appears in the task's title
// or description, or names the beginning of its ID. Labels have a filter of
// their own and comments are not searched, so those are deliberately absent.
// Title and description are joined with a newline so a term cannot straddle the
// two. An empty query matches.
//
// keys is every key the project has ever minted under, active and retired
// alike: a retired key's tasks are still tasks, so a pasted ID under one still
// has to find its task. Each term matches by either route and every term must
// match, so "WB-01M2 notarize" is one term answered by an ID and one by a title.
func MatchesFind(task Task, query string, keys []string) bool {
	terms := FindTerms(query)
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(task.Title + "\n" + task.Description)
	for _, term := range terms {
		if strings.Contains(haystack, term) {
			continue
		}
		if prefix, ok := FindKeyPrefixTerm(term, keys); ok && strings.HasPrefix(task.ID, prefix) {
			continue
		}
		return false
	}
	return true
}
