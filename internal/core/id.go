package core

import (
	"crypto/rand"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

var projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

type IDSource interface {
	New() (string, error)
}

type IDSourceFunc func() (string, error)

func (f IDSourceFunc) New() (string, error) {
	return f()
}

type CryptoULIDSource struct {
	Now     func() time.Time
	Entropy io.Reader
}

func (s CryptoULIDSource) New() (string, error) {
	now := s.Now
	if now == nil {
		now = time.Now
	}

	entropy := s.Entropy
	if entropy == nil {
		entropy = rand.Reader
	}

	id, err := ulid.New(ulid.Timestamp(now().UTC()), entropy)
	if err != nil {
		return "", Wrap(CategoryOperational, "cannot generate ULID", err)
	}
	return id.String(), nil
}

func ValidateProjectKey(key string) error {
	if !projectKeyPattern.MatchString(key) {
		return Errorf(CategoryValidation, "project key %q must match %s", key, projectKeyPattern)
	}
	return nil
}

// ProjectKeyPattern is the grammar a project key has to match, for a message
// that tells someone what to type.
func ProjectKeyPattern() string {
	return projectKeyPattern.String()
}

// DefaultProjectKey is the key a project gets when nothing better can be
// derived from where it lives. It was every project's key before setup began
// asking, so an old script that never passed --key still lands here when its
// directory name offers nothing.
const DefaultProjectKey = "WB"

// projectKeyMaximum is the longest key projectKeyPattern admits.
const projectKeyMaximum = 10

// wordBoundary finds the lowercase-or-digit-then-uppercase transitions in a
// directory name (MyAppService) that DeriveProjectKey reads as word starts,
// the same runs the desktop's suggestKey looks for before it splits on
// punctuation.
var wordBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// wordSeparator is everything DeriveProjectKey treats as space between words:
// any run of characters outside ASCII letters and digits, non-ASCII runes
// included. Splitting on it is what makes café and 日本語 read as word breaks
// rather than as letters worth keeping.
var wordSeparator = regexp.MustCompile(`[^A-Za-z0-9]+`)

// DeriveProjectKey proposes a project key from a directory name. This is a
// direct port of the desktop app's suggestKey in
// desktop/src/main/discovery.js (minus the taken-set collision handling,
// which only matters when importing several repositories at once): split the
// last path element into words at camelCase and punctuation boundaries, take
// each word's first letter when there are two or more words or the first four
// letters of the one word there is, uppercase it, pad short results with
// DefaultProjectKey and prefix results that do not start with a letter. The
// two implementations suggest a key for the same reason — a project owner
// picking one at setup or at import time — and must change together.
//
// Only ASCII survives into the key itself, even though non-ASCII runes still
// count as word breaks. A key is typed into task IDs and shell commands by
// every collaborator, and a project named in another script is better served
// by choosing its key at the prompt than by a transliteration this cannot get
// right for every language.
func DeriveProjectKey(name string) string {
	name = strings.TrimRight(name, "/\\")
	if index := strings.LastIndexAny(name, "/\\"); index >= 0 {
		name = name[index+1:]
	}

	spaced := wordBoundary.ReplaceAllString(name, "$1 $2")
	words := wordSeparator.Split(spaced, -1)

	var base strings.Builder
	switch nonEmpty := nonEmptyWords(words); len(nonEmpty) {
	case 0:
		// base stays empty; the padding below turns it into DefaultProjectKey.
	case 1:
		word := nonEmpty[0]
		if len(word) > 4 {
			word = word[:4]
		}
		base.WriteString(strings.ToUpper(word))
	default:
		for _, word := range nonEmpty {
			base.WriteString(strings.ToUpper(word[:1]))
		}
	}

	key := base.String()
	if len(key) < 2 {
		key = (key + DefaultProjectKey)[:2]
	}
	if len(key) > projectKeyMaximum {
		key = key[:projectKeyMaximum]
	}
	if key[0] < 'A' || key[0] > 'Z' {
		key = "W" + key
		if len(key) > projectKeyMaximum {
			key = key[:projectKeyMaximum]
		}
	}
	return key
}

// nonEmptyWords drops the empty strings regexp.Split leaves between adjacent
// separators (or at either end), the same filtering suggestKey's .filter(Boolean)
// does after its split.
func nonEmptyWords(words []string) []string {
	kept := make([]string, 0, len(words))
	for _, word := range words {
		if word != "" {
			kept = append(kept, word)
		}
	}
	return kept
}

// ValidateProjectID reports whether a project ID is a canonical uppercase
// ULID. It is the one rule for a project ID wherever one is stored: the
// tracked configuration, the private guard, and the identity document all
// share it so a value one accepts cannot be rejected by another.
func ValidateProjectID(projectID string) error {
	parsed, err := ulid.ParseStrict(projectID)
	if err != nil {
		return Wrap(CategoryValidation, "project ID must contain a canonical ULID", err)
	}
	if parsed.String() != projectID {
		return Errorf(CategoryValidation, "project ID must contain a canonical uppercase ULID")
	}
	return nil
}

// ulidShapePattern matches a canonical ULID body's length and alphabet without
// decoding it, and without insisting on the canonical uppercase form. It is
// deliberately looser than ulid.ParseStrict: it answers "could another
// Workbook have written this", where accepting one name too many costs a piece
// of advice and rejecting one too few costs somebody's history. KeySet's
// PlausibleTaskID is its one reader.
var ulidShapePattern = regexp.MustCompile(`(?i)^[0-9A-HJKMNP-TV-Z]{26}$`)

// peeledRefSuffix is what git ls-remote appends to the extra record naming the
// object an annotated tag points at. Such a record never names a task Workbook
// will read, but the name inside it is still a task's name, and the gate in
// front of destructive advice has to answer for the name it is given.
const peeledRefSuffix = "^{}"

// ParseTaskID splits a task ID into its project key and its ULID body, and
// reports whether it is shaped like a task ID at all.
//
// It answers about shape and never about ownership: the key has to match the
// project-key grammar and the body has to be a canonical uppercase ULID, and
// which keys this project actually has is core.KeySet's question. That split is
// what lets the task fold stay total over a teammate's history — a pack naming a
// key this clone has not fetched the ledger for is unfamiliar, not corrupt, the
// same reading NormalizeTask already gives a stored status and a stored
// priority.
func ParseTaskID(taskID string) (string, string, bool) {
	key, body, separated := strings.Cut(taskID, "-")
	if !separated {
		return "", "", false
	}
	if err := ValidateProjectKey(key); err != nil {
		return "", "", false
	}
	parsed, err := ulid.ParseStrict(body)
	if err != nil || parsed.String() != body {
		return "", "", false
	}
	return key, body, true
}

// ValidateTaskIDShape is ParseTaskID with a message, for the durable documents
// and the plumbing that has to say why a name was refused.
func ValidateTaskIDShape(taskID string) error {
	key, body, separated := strings.Cut(taskID, "-")
	if !separated {
		return Errorf(CategoryValidation, "task ID %q must be <KEY>-<ULID>", taskID)
	}
	if err := ValidateProjectKey(key); err != nil {
		return err
	}
	parsed, err := ulid.ParseStrict(body)
	if err != nil {
		return Wrap(CategoryValidation, "task ID must contain a canonical ULID", err)
	}
	if parsed.String() != body {
		return Errorf(CategoryValidation, "task ID must contain a canonical uppercase ULID")
	}
	return nil
}
